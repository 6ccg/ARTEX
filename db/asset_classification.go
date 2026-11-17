package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
)

// migrateAssetClassification runs once inside the existing schema transaction.
// No aliases or duplicate asset stores remain after references have been moved.
func migrateAssetClassification(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT row_to_json(a) FROM assets a WHERE type IN ('root_domain','subdomain','endpoint') OR (type='service' AND service_type='http') ORDER BY CASE WHEN type='endpoint' THEN 0 ELSE 1 END,last_seen,id`)
	if err != nil {
		return err
	}
	var assets []Asset
	for rows.Next() {
		var raw []byte
		var a Asset
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			rows.Close()
			return err
		}
		assets = append(assets, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	store := &AssetStore{tx: tx}
	for _, a := range assets {
		var target int64
		if a.Type == "endpoint" {
			// A historical URL without a scheme cannot be assigned http/https
			// without evidence. Preserve it verbatim instead of guessing.
			if _, parseErr := HTTPServiceURL(a.URL); parseErr != nil {
				continue
			}
			if a.Method == "" {
				a.Method = "UNKNOWN"
			}
			target, err = store.UpsertEndpoint(UpsertEndpointReq{URL: a.URL, Method: a.Method, Params: a.Params, IP: a.IP})
		} else if a.Type == "service" {
			target, err = store.UpsertHTTPService(UpsertHTTPServiceReq{URL: a.URL, IP: a.IP, Technologies: a.Technologies, StatusCode: a.StatusCode, ContentLength: a.ContentLength, PageTitle: a.PageTitle, FaviconMMH3: a.FaviconMMH3, Auth: a.Auth})
			if err == nil {
				var endpoint int64
				endpoint, err = store.httpServiceEndpoint(a.URL, 0)
				if err == nil && endpoint > 0 {
					err = copyAssetReferences(tx, a.ID, endpoint)
				}
			}
		} else if ip := net.ParseIP(a.Domain); ip != nil {
			target, err = store.UpsertIP(UpsertIPReq{IP: ip.String()})
		} else {
			continue
		}
		if err != nil {
			return fmt.Errorf("classify asset %d: %w", a.ID, err)
		}
		if target == a.ID {
			continue
		}
		if err := mergeAssetReferences(tx, a.ID, target); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE assets SET root_domain=NULL WHERE try_inet(root_domain) IS NOT NULL;
DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='assets'::regclass AND conname='assets_domain_not_ip') THEN
ALTER TABLE assets ADD CONSTRAINT assets_domain_not_ip CHECK (type NOT IN ('root_domain','subdomain') OR try_inet(domain) IS NULL);
END IF; END $$`)
	return err
}

func copyAssetReferences(tx *sql.Tx, from, to int64) error {
	if from == to {
		return nil
	}
	_, err := tx.Exec(`INSERT INTO exploration_anchors(node_id,asset_id) SELECT node_id,$2 FROM exploration_anchors WHERE asset_id=$1 ON CONFLICT DO NOTHING`, from, to)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO task_asset_links(task_id,asset_id,source,source_summary,source_node_id,created_at,updated_at)
SELECT task_id,$2,source,source_summary,source_node_id,created_at,updated_at FROM task_asset_links WHERE asset_id=$1
ON CONFLICT(task_id,asset_id) DO UPDATE SET
 source=CASE WHEN task_asset_links.source='manual' THEN task_asset_links.source ELSE EXCLUDED.source END,
 source_summary=CASE WHEN task_asset_links.source_summary='' THEN EXCLUDED.source_summary ELSE task_asset_links.source_summary END,
 source_node_id=COALESCE(task_asset_links.source_node_id,EXCLUDED.source_node_id),
 created_at=LEAST(task_asset_links.created_at,EXCLUDED.created_at)`, from, to)
	return err
}

func mergeAssetReferences(tx *sql.Tx, from, to int64) error {
	if err := copyAssetReferences(tx, from, to); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE assets target SET
 extra=source.extra || target.extra,
 company_id=CASE WHEN source.company_source='explicit' AND source.company_id IS NOT NULL AND target.company_source<>'explicit' THEN source.company_id ELSE COALESCE(target.company_id,source.company_id) END,
 company_source=CASE WHEN source.company_source='explicit' AND source.company_id IS NOT NULL THEN 'explicit' ELSE target.company_source END,
 created_at=LEAST(target.created_at,source.created_at),last_seen=GREATEST(target.last_seen,source.last_seen)
FROM assets source WHERE source.id=$1 AND target.id=$2`, from, to); err != nil {
		return err
	}
	// These JSON arrays are also live references, unlike historical activity text.
	for _, table := range []string{"findings", "task_intelligence"} {
		if _, err := tx.Exec(`UPDATE `+table+` SET asset_ids=(SELECT jsonb_agg(DISTINCT CASE WHEN e=to_jsonb($1::bigint) THEN to_jsonb($2::bigint) WHEN e=to_jsonb($1::text) THEN to_jsonb($2::text) ELSE e END) FROM jsonb_array_elements(asset_ids) e)
WHERE asset_ids @> jsonb_build_array($1::bigint) OR asset_ids @> jsonb_build_array($1::text)`, from, to); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE exploration_nodes SET content_version=content_version+1,payload=jsonb_set(payload::jsonb,'{asset_ids}',
(SELECT jsonb_agg(DISTINCT CASE WHEN e=to_jsonb($1::bigint) THEN to_jsonb($2::bigint) WHEN e=to_jsonb($1::text) THEN to_jsonb($2::text) ELSE e END) FROM jsonb_array_elements(payload::jsonb->'asset_ids') e))
WHERE jsonb_typeof(payload::jsonb->'asset_ids')='array' AND ((payload::jsonb->'asset_ids') @> jsonb_build_array($1::bigint) OR (payload::jsonb->'asset_ids') @> jsonb_build_array($1::text))`, from, to); err != nil {
		return err
	}
	_, err := tx.Exec(`DELETE FROM assets WHERE id=$1`, from)
	return err
}
