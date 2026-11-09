package controller

import (
	"context"
	"encoding/json"

	"hostlier/internal/store"
)

func collectionValues[T any](ctx context.Context, s *store.Store, name string, query store.CollectionQuery) ([]T, error) {
	items, err := s.Collection(ctx, name, query)
	if err != nil {
		return nil, err
	}
	values := make([]T, 0, len(items))
	for _, item := range items {
		var value T
		if err := json.Unmarshal(item.Payload, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

func collectionMap[T any](ctx context.Context, s *store.Store, name string, query store.CollectionQuery) (map[string]T, error) {
	items, err := s.Collection(ctx, name, query)
	if err != nil {
		return nil, err
	}
	values := make(map[string]T, len(items))
	for _, item := range items {
		var value T
		if err := json.Unmarshal(item.Payload, &value); err != nil {
			return nil, err
		}
		values[item.Key] = value
	}
	return values, nil
}
