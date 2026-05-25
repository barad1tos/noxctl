// Package recommend infers the fitting blueprint for a Bear tag from
// structural metrics. Rule-based and explainable: an ordered decision tree,
// no scoring weights, no ML. Pure + read-only — callers emit the result as
// candidate config.
package recommend

// Confidence grades how cleanly the deciding metric cleared its threshold.
type Confidence int

const (
	Low Confidence = iota
	Medium
	High
)

func (c Confidence) String() string {
	switch c {
	case High:
		return "high"
	case Medium:
		return "medium"
	default:
		return "low"
	}
}
