package normalizer

import (
	"strings"
	"sync"
)

const (
	defaultSimilarityThreshold = 0.5
	defaultMaxClusters         = 1000
	wildcardToken              = "<*>"
)

// LogCluster represents a group of log lines sharing the same template.
type LogCluster struct {
	ID       int
	Template []string
	Count    int64
}

// DrainConfig configures the Drain miner behavior.
type DrainConfig struct {
	SimilarityThreshold float64
	MaxClusters         int
}

// DrainMiner is a minimal Drain-style log template miner.
type DrainMiner struct {
	mu           sync.RWMutex
	clusters     []*LogCluster
	maxClusters  int
	simThreshold float64
	nextID       int
}

// NewDrainMiner creates a DrainMiner with the given configuration.
func NewDrainMiner(cfg DrainConfig) *DrainMiner {
	threshold := cfg.SimilarityThreshold
	if threshold <= 0 {
		threshold = defaultSimilarityThreshold
	}
	maxClusters := cfg.MaxClusters
	if maxClusters <= 0 {
		maxClusters = defaultMaxClusters
	}
	return &DrainMiner{
		clusters:     make([]*LogCluster, 0),
		maxClusters:  maxClusters,
		simThreshold: threshold,
		nextID:       1,
	}
}

// Process tokenizes the line, finds or creates a matching cluster, and returns it.
func (d *DrainMiner) Process(line string) *LogCluster {
	tokens := tokenize(line)
	if len(tokens) == 0 {
		return nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	bestCluster, bestSim := d.findBestMatch(tokens)

	if bestCluster != nil && bestSim >= d.simThreshold {
		bestCluster.Count++
		updateTemplate(bestCluster, tokens)
		return bestCluster
	}

	if len(d.clusters) >= d.maxClusters {
		d.evict()
	}

	cluster := &LogCluster{
		ID:       d.nextID,
		Template: make([]string, len(tokens)),
		Count:    1,
	}
	copy(cluster.Template, tokens)
	d.nextID++
	d.clusters = append(d.clusters, cluster)
	return cluster
}

// Match performs a read-only lookup for the best matching cluster without mutation.
func (d *DrainMiner) Match(line string) (*LogCluster, bool) {
	tokens := tokenize(line)
	if len(tokens) == 0 {
		return nil, false
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	bestCluster, bestSim := d.findBestMatch(tokens)
	if bestCluster != nil && bestSim >= d.simThreshold {
		return bestCluster, true
	}
	return nil, false
}

func (d *DrainMiner) findBestMatch(tokens []string) (*LogCluster, float64) {
	var bestCluster *LogCluster
	var bestSim float64

	for _, c := range d.clusters {
		if len(c.Template) != len(tokens) {
			continue
		}
		sim := similarity(c.Template, tokens)
		if sim > bestSim {
			bestSim = sim
			bestCluster = c
		}
	}
	return bestCluster, bestSim
}

func similarity(a, b []string) float64 {
	if len(a) != len(b) {
		return 0
	}
	if len(a) == 0 {
		return 0
	}
	matches := 0
	for i := range a {
		if a[i] == wildcardToken || a[i] == b[i] {
			matches++
		}
	}
	return float64(matches) / float64(len(a))
}

func updateTemplate(cluster *LogCluster, tokens []string) {
	for i := range cluster.Template {
		if cluster.Template[i] != tokens[i] && cluster.Template[i] != wildcardToken {
			cluster.Template[i] = wildcardToken
		}
	}
}

func (d *DrainMiner) evict() {
	if len(d.clusters) == 0 {
		return
	}
	minIdx := 0
	minCount := d.clusters[0].Count
	for i, c := range d.clusters {
		if c.Count < minCount {
			minCount = c.Count
			minIdx = i
		}
	}
	d.clusters = append(d.clusters[:minIdx], d.clusters[minIdx+1:]...)
}

func tokenize(line string) []string {
	return strings.Fields(line)
}
