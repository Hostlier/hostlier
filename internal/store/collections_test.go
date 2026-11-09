package store

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCollectionIndexFiltersOrdersAndRebuilds(t *testing.T) {
	dir := t.TempDir()
	eventFixtures := make([]map[string]string, 12)
	for i := range eventFixtures {
		operation := "one"
		if i%2 == 1 { operation = "two" }
		eventFixtures[i] = map[string]string{"operationId": operation}
	}
	state := map[string]any{
		"domains": []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven"},
		"events": eventFixtures,
		"applications": map[string]any{
			"z-running": map[string]string{"name": "z-running", "status": "running"},
			"a-deleted": map[string]string{"name": "a-deleted", "status": "deleted"},
			"b-running": map[string]string{"name": "b-running", "status": "running"},
		},
		"tunnels": map[string]any{
			"z": map[string]string{"name": "z", "owner": "alice"},
			"a": map[string]string{"name": "a", "owner": "bob"},
			"b": map[string]string{"name": "b", "owner": "alice"},
		},
	}
	s, err := Open(dir, &state)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	apps, err := s.Collection(ctx, "applications", CollectionQuery{ExcludeStatus: "deleted", Limit: 1})
	if err != nil || len(apps) != 1 || apps[0].Key != "b-running" {
		t.Fatalf("filtered apps: %v %v", apps, err)
	}
	tunnels, err := s.Collection(ctx, "tunnels", CollectionQuery{Owner: "alice"})
	if err != nil || len(tunnels) != 2 || tunnels[0].Key != "b" || tunnels[1].Key != "z" {
		t.Fatalf("owner tunnel order: %v %v", tunnels, err)
	}
	events, err := s.Collection(ctx, "events", CollectionQuery{Operation: "two", Tail: true, Limit: 2})
	if err != nil || len(events) != 2 || events[0].Key != "9" || events[1].Key != "11" {
		t.Fatalf("filtered event tail: %v %v", events, err)
	}
	domains, err := s.Collection(ctx, "domains", CollectionQuery{})
	if err != nil || len(domains) != 11 || domains[10].Key != "10" {
		t.Fatalf("domain order: %v %v", domains, err)
	}
	delete(state["applications"].(map[string]any), "b-running")
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var restored map[string]json.RawMessage
	s, err = Open(dir, &restored)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	apps, err = s.Collection(ctx, "applications", CollectionQuery{ExcludeStatus: "deleted"})
	if err != nil || len(apps) != 1 || apps[0].Key != "z-running" {
		t.Fatalf("reopened index: %v %v", apps, err)
	}
}
