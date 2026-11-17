package db

import (
	"context"
	"fmt"
)

const historicalGraphNode = `(kind<>'intent' OR state IN ('done','blocked','exhausted','stopped'))`

// GraphNodePage bounds transport without truncating a growing graph. IDs are
// global, so one cursor also orders nodes from directly related explorations.
func (s *ExplorationStore) GraphNodePage(after int64, size int, historical bool) ([]*Node, bool, error) {
	return s.GraphNodeRange(context.Background(), after, 0, size, historical)
}

// GraphNodeRange also bounds the end when the client requests an indexed range.
func (s *ExplorationStore) GraphNodeRange(ctx context.Context, after, through int64, size int, historical bool) ([]*Node, bool, error) {
	filter := ""
	if historical {
		filter = " AND " + historicalGraphNode
	}
	args := []any{s.expID, after, size + 1}
	if through > 0 {
		filter += " AND id<=$4"
		args = append(args, through)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+nodeCols+` FROM exploration_nodes WHERE exploration_id=$1 AND id>$2`+filter+` ORDER BY id LIMIT $3`, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	nodes, err := scanNodes(rows)
	if err != nil {
		return nil, false, err
	}
	more := len(nodes) > size
	if more {
		nodes = nodes[:size]
	}
	return nodes, more, nil
}

type GraphRange struct {
	After    int64  `json:"after,string"`
	Through  int64  `json:"through,string"`
	Revision string `json:"revision"`
}

// GraphIndex fingerprints the node and edge rows of each range without reading
// the payloads. xmin changes on an update, including changes to visibility.
func (s *ExplorationStore) GraphIndex(ctx context.Context, sources []DirectSourceStore, size int) ([]GraphRange, error) {
	explorations, taskIDs := []int64{s.expID}, []int64{0}
	for _, source := range sources {
		explorations = append(explorations, source.Store.ID())
		taskIDs = append(taskIDs, source.Task.TaskID)
	}
	rows, err := s.db.QueryContext(ctx, `
WITH scope AS (
 SELECT * FROM unnest($1::bigint[],$2::bigint[]) AS s(exploration_id,task_id)
), nodes AS MATERIALIZED (
 SELECT n.id,n.exploration_id,n.xmin::text AS version,s.task_id,
        (row_number() OVER (ORDER BY n.id)-1)/$3 AS slot
 FROM exploration_nodes n JOIN scope s USING(exploration_id)
 WHERE s.task_id=0 OR `+historicalGraphNode+`
), node_versions AS (
 SELECT slot,min(id)-1 AS after,max(id) AS through,
        md5(string_agg(concat_ws(':',id,task_id,version),',' ORDER BY id)) AS revision
 FROM nodes GROUP BY slot
), edge_versions AS (
 SELECT ranges.slot,md5(string_agg(concat_ws(':',e.src_id,e.rel,e.dst_id,src.id IS NOT NULL,dst.id IS NOT NULL),',' ORDER BY e.src_id,e.rel,e.dst_id)) AS revision
 FROM exploration_edges e JOIN scope s USING(exploration_id)
 LEFT JOIN nodes src ON src.id=e.src_id AND src.exploration_id=e.exploration_id
 LEFT JOIN nodes dst ON dst.id=e.dst_id AND dst.exploration_id=e.exploration_id
 CROSS JOIN LATERAL (VALUES (src.slot),(CASE WHEN dst.slot IS DISTINCT FROM src.slot THEN dst.slot END)) ranges(slot)
 WHERE ranges.slot IS NOT NULL
 GROUP BY ranges.slot
)
SELECT after,through,n.revision||COALESCE(e.revision,'')
FROM node_versions n LEFT JOIN edge_versions e USING(slot)
ORDER BY after`, explorations, taskIDs, size)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GraphRange{}
	for rows.Next() {
		var r GraphRange
		if err := rows.Scan(&r.After, &r.Through, &r.Revision); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Return all edges touching this node page; the client deduplicates page overlap.
// Historical visibility is checked against both endpoints, across node pages.
func (s *ExplorationStore) GraphPageEdges(ids []int64, historical bool) ([]Edge, error) {
	if len(ids) == 0 {
		return []Edge{}, nil
	}
	filter := ""
	if historical {
		for _, column := range []string{"src_id", "dst_id"} {
			filter += fmt.Sprintf(` AND EXISTS(SELECT 1 FROM exploration_nodes WHERE id=e.%s AND %s)`, column, historicalGraphNode)
		}
	}
	rows, err := s.db.Query(`SELECT src_id,rel,dst_id FROM exploration_edges e WHERE exploration_id=$1 AND (src_id=ANY($2::bigint[]) OR dst_id=ANY($2::bigint[]))`+filter, s.expID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	edges := []Edge{}
	for rows.Next() {
		var edge Edge
		if err := rows.Scan(&edge.From, &edge.Rel, &edge.To); err != nil {
			return nil, err
		}
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}
