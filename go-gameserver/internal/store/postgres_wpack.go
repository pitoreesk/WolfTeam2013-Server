package store

import (
	"context"
	"fmt"
)

// WPackContentStore loads the server-defined contents of 2012 W-Pack shop
// items (14901..15000); no client data file defines them.
type WPackContentStore interface {
	WPackContents(context.Context) (map[int][]PurchaseComponent, error)
}

func (s *PostgresStore) WPackContents(ctx context.Context) (map[int][]PurchaseComponent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT pack_item_id, item_id, days FROM wpack_contents
		ORDER BY pack_item_id, position`)
	if err != nil {
		return nil, fmt.Errorf("query W-Pack contents: %w", err)
	}
	defer rows.Close()
	contents := make(map[int][]PurchaseComponent)
	for rows.Next() {
		var pack, item, days int
		if err := rows.Scan(&pack, &item, &days); err != nil {
			return nil, fmt.Errorf("scan W-Pack content: %w", err)
		}
		contents[pack] = append(contents[pack], PurchaseComponent{ItemID: item, Days: days})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate W-Pack contents: %w", err)
	}
	return contents, nil
}
