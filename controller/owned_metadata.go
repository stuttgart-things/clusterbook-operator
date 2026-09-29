package controller

import (
	"sort"
	"strings"
)

// The operator only ever added or overwrote labels/annotations on the ArgoCD
// cluster Secret, so a key dropped from spec.labels / spec.annotations (or a
// cleared spec.clusterType, …) stayed on the Secret forever. With
// ApplicationSets selecting on `NotIn ["false"]`, a stale "false" toggle kept
// a cluster from ever getting that Application (issue #121).
//
// The fix: record on the Secret which keys the operator wrote last time, and
// on the next reconcile delete every recorded key that is no longer desired.
// Keys set by anyone else are never recorded and so never touched.
const (
	annotationOwnedLabels      = clusterbookPrefix + "owned-labels"
	annotationOwnedAnnotations = clusterbookPrefix + "owned-annotations"
)

// applyOwned deletes from current every key named in prevRecord that desired
// no longer carries, then merges desired into current.
func applyOwned(current map[string]string, prevRecord string, desired map[string]string) {
	for _, k := range parseOwnedRecord(prevRecord) {
		if _, keep := desired[k]; !keep {
			delete(current, k)
		}
	}
	for k, v := range desired {
		current[k] = v
	}
}

// recordOwned writes the ownership records for the given desired label and
// annotation sets onto annotations. Call after applyOwned for both maps, so
// the previous records have already been read.
func recordOwned(annotations, labels, desiredAnnotations map[string]string) {
	annotations[annotationOwnedLabels] = ownedRecord(labels)
	annotations[annotationOwnedAnnotations] = ownedRecord(desiredAnnotations)
}

// ownedRecord serialises the keys of m as a sorted, comma-separated list.
// Label and annotation keys cannot contain commas, so no escaping is needed.
func ownedRecord(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

func parseOwnedRecord(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
