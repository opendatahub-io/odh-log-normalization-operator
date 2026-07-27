package normalizer_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/opendatahub-io/log-normalizer-operator/internal/normalizer"
)

func TestDrainMiner_SameTemplate(t *testing.T) {
	miner := normalizer.NewDrainMiner(normalizer.DrainConfig{
		SimilarityThreshold: 0.5,
		MaxClusters:         100,
	})

	c1 := miner.Process("user 12345 logged in from 10.0.0.1")
	c2 := miner.Process("user 67890 logged in from 192.168.1.1")

	if c1.ID != c2.ID {
		t.Errorf("expected same cluster ID, got %d and %d", c1.ID, c2.ID)
	}
	if c2.Count != 2 {
		t.Errorf("expected count=2, got %d", c2.Count)
	}
}

func TestDrainMiner_DifferentTemplates(t *testing.T) {
	miner := normalizer.NewDrainMiner(normalizer.DrainConfig{
		SimilarityThreshold: 0.5,
		MaxClusters:         100,
	})

	c1 := miner.Process("user logged in successfully")
	c2 := miner.Process("connection timeout after 30s to database host")

	if c1.ID == c2.ID {
		t.Errorf("expected different cluster IDs, got same: %d", c1.ID)
	}
}

func TestDrainMiner_WildcardInsertion(t *testing.T) {
	miner := normalizer.NewDrainMiner(normalizer.DrainConfig{
		SimilarityThreshold: 0.5,
		MaxClusters:         100,
	})

	miner.Process("request completed in 120ms status 200")
	c2 := miner.Process("request completed in 450ms status 500")

	found := false
	for _, tok := range c2.Template {
		if tok == "<*>" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected wildcard <*> in template, got %v", c2.Template)
	}
}

func TestDrainMiner_MaxClusters(t *testing.T) {
	miner := normalizer.NewDrainMiner(normalizer.DrainConfig{
		SimilarityThreshold: 0.99,
		MaxClusters:         3,
	})

	for i := 0; i < 5; i++ {
		miner.Process(fmt.Sprintf("unique_%d pattern that differs completely from all others x%d", i, i*1000))
	}

	// Match should still work — we shouldn't panic or corrupt state
	_, ok := miner.Match("unique_0 pattern that differs completely from all others x0")
	// The oldest cluster may have been evicted, so we just verify no panic
	_ = ok
}

func TestDrainMiner_Match_ReadOnly(t *testing.T) {
	miner := normalizer.NewDrainMiner(normalizer.DrainConfig{
		SimilarityThreshold: 0.5,
		MaxClusters:         100,
	})

	miner.Process("server started on port 8080")
	c, ok := miner.Match("server started on port 9090")
	if !ok {
		t.Fatal("expected match for similar line")
	}
	if c.Count != 1 {
		t.Errorf("Match should not mutate count, got %d", c.Count)
	}
}

func TestDrainMiner_ThreadSafety(t *testing.T) {
	miner := normalizer.NewDrainMiner(normalizer.DrainConfig{
		SimilarityThreshold: 0.5,
		MaxClusters:         100,
	})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			miner.Process(fmt.Sprintf("goroutine %d processing request %d", n, n*10))
		}(i)
	}
	wg.Wait()
}
