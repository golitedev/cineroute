package requests

import (
	"cineroute/internal/tmdb"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkerUpdatesPreserveNewSeasonsAndCancellation(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "requests.db"))
	settings := Defaults()
	original, err := s.Create(Job{Owner: "Alice", Media: tmdb.Details{Result: tmdb.Result{ID: 1, MediaType: "tv"}}, Target: "remote", Seasons: []int{1}, Status: "queued"}, settings, false)
	if err != nil {
		t.Fatal(err)
	}
	expanded := original
	expanded.Seasons = []int{2}
	expanded.FollowFuture = true
	if _, err := s.Create(expanded, settings, false); err != nil {
		t.Fatal(err)
	}
	original.Status = "waiting"
	original.NextSearch = time.Now().Add(time.Hour)
	if err := s.Update(original); err != nil {
		t.Fatal(err)
	}
	current, err := s.Job(original.ID)
	if err != nil || len(current.Seasons) != 2 || !current.FollowFuture || current.Status != "queued" || !current.NextSearch.Before(original.NextSearch) {
		t.Fatalf("lost concurrent addition: %+v %v", current, err)
	}
	if _, err := s.Action(original.ID, "", "retry"); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(original); err != nil {
		t.Fatal(err)
	}
	current, _ = s.Job(original.ID)
	if current.RetryAt.IsZero() {
		t.Fatal("worker overwrote the administrator's retry")
	}
	if _, err := s.Action(original.ID, "alice", "cancel"); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(original); err != nil {
		t.Fatal(err)
	}
	current, _ = s.Job(original.ID)
	if current.Status != "cancelled" {
		t.Fatalf("withdrawn job resurrected: %+v", current)
	}
}

func TestWorkerLeaseAndPlacementPersistAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.db")
	first, second := NewStore(path), NewStore(path)
	if ok, err := first.Lease("worker-one", false); err != nil || !ok {
		t.Fatalf("first lease %v %v", ok, err)
	}
	if ok, err := second.Lease("worker-two", false); err != nil || ok {
		t.Fatalf("duplicate worker lease %v %v", ok, err)
	}
	if err := first.Pin("tv", 1, "disk2"); err != nil {
		t.Fatal(err)
	}
	if err := second.Pin("tv", 1, "disk1"); err == nil {
		t.Fatal("show moved between disks")
	}
	if disk, err := second.Placement("tv", 1); err != nil || disk != "disk2" {
		t.Fatalf("lost placement: %s %v", disk, err)
	}
	if _, err := first.Lease("worker-one", true); err != nil {
		t.Fatal(err)
	}
	if ok, err := second.Lease("worker-two", false); err != nil || !ok {
		t.Fatalf("takeover %v %v", ok, err)
	}
}
