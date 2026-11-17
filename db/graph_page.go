package db

import "fmt"

const historicalGraphNode = `(kind<>'intent' OR state IN ('done','blocked','exhausted','stopped'))`

// GraphNodePage bounds transport without truncating a growing graph. IDs are
// global, so one cursor also orders nodes from directly related explorations.
func (s *ExplorationStore) GraphNodePage(after int64, size int, historical bool) ([]*Node, bool, error) {
	filter := ""
	if historical {
		filter = " AND " + historicalGraphNode
	}
	rows, err := s.db.Query(`SELECT `+nodeCols+` FROM exploration_nodes WHERE exploration_id=$1 AND id>$2`+filter+` ORDER BY id LIMIT $3`, s.expID, after, size+1)
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
