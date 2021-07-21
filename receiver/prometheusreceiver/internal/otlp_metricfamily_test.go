// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//       http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package internal

import (
	"fmt"
	"testing"

	"github.com/prometheus/prometheus/pkg/labels"
	"github.com/prometheus/prometheus/pkg/textparse"
	"github.com/prometheus/prometheus/scrape"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"go.opentelemetry.io/collector/model/pdata"
)

type byLookupMetadataCache map[string]scrape.MetricMetadata

func (bmc byLookupMetadataCache) Metadata(familyName string) (scrape.MetricMetadata, bool) {
	lookup, ok := bmc[familyName]
	return lookup, ok
}

func (bmc byLookupMetadataCache) SharedLabels() labels.Labels {
	return nil
}

var mc = byLookupMetadataCache{
	"counter": scrape.MetricMetadata{
		Metric: "cr",
		Type:   textparse.MetricTypeCounter,
		Help:   "This is some help",
		Unit:   "By",
	},
	"gauge": scrape.MetricMetadata{
		Metric: "ge",
		Type:   textparse.MetricTypeGauge,
		Help:   "This is some help",
		Unit:   "1",
	},
	"gaugehistogram": scrape.MetricMetadata{
		Metric: "gh",
		Type:   textparse.MetricTypeGaugeHistogram,
		Help:   "This is some help",
		Unit:   "?",
	},
	"histogram": scrape.MetricMetadata{
		Metric: "hg",
		Type:   textparse.MetricTypeHistogram,
		Help:   "This is some help",
		Unit:   "ms",
	},
	"summary": scrape.MetricMetadata{
		Metric: "s",
		Type:   textparse.MetricTypeSummary,
		Help:   "This is some help",
		Unit:   "?",
	},
	"unknown": scrape.MetricMetadata{
		Metric: "u",
		Type:   textparse.MetricTypeUnknown,
		Help:   "This is some help",
		Unit:   "?",
	},
}

func TestIsCumulativeEquivalence(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "counter", want: true},
		{name: "gauge", want: false},
		{name: "histogram", want: true},
		{name: "gaugehistogram", want: false},
		{name: "does not exist", want: false},
		{name: "unknown", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			mf := newMetricFamily(tt.name, mc, zap.NewNop()).(*metricFamily)
			mfp := newMetricFamilyPdata(tt.name, mc).(*metricFamilyPdata)
			assert.Equal(t, mf.isCumulativeType(), mfp.isCumulativeTypePdata(), "mismatch in isCumulative")
			assert.Equal(t, mf.isCumulativeType(), tt.want, "isCumulative does not match for regular metricFamily")
			assert.Equal(t, mfp.isCumulativeTypePdata(), tt.want, "isCumulative does not match for pdata metricFamily")
		})
	}
}

func TestMetricGroupData_toDistributionUnitTest(t *testing.T) {
	type scrape struct {
		at     int64
		value  float64
		metric string
	}
	tests := []struct {
		name    string
		labels  labels.Labels
		scrapes []*scrape
		want    func() pdata.HistogramDataPoint
	}{
		{
			name:   "histogram",
			labels: labels.Labels{{Name: "a", Value: "A"}, {Name: "le", Value: "0.75"}, {Name: "b", Value: "B"}},
			scrapes: []*scrape{
				{at: 11, value: 10, metric: "histogram_count"},
				{at: 11, value: 1004.78, metric: "histogram_sum"},
				{at: 13, value: 33.7, metric: "value"},
			},
			want: func() pdata.HistogramDataPoint {
				point := pdata.NewHistogramDataPoint()
				point.SetCount(10)
				point.SetSum(1004.78)
				point.SetTimestamp(11 * 1e6) // the time in milliseconds -> nanoseconds.
				point.SetBucketCounts([]uint64{33})
				point.SetExplicitBounds([]float64{})
				point.SetStartTimestamp(11 * 1e6)
				labelsMap := point.LabelsMap()
				labelsMap.Insert("a", "A")
				labelsMap.Insert("b", "B")
				return point
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			mp := newMetricFamilyPdata(tt.name, mc).(*metricFamilyPdata)
			for _, tv := range tt.scrapes {
				require.NoError(t, mp.Add(tv.metric, tt.labels.Copy(), tv.at, tv.value))
			}

			require.Equal(t, 1, len(mp.groups), "Expecting exactly 1 groupKey")
			groupKey := mp.getGroupKey(tt.labels.Copy())
			require.NotNil(t, mp.groups[groupKey], "Expecting the groupKey to have a value given key:: "+groupKey)

			hdpL := pdata.NewHistogramDataPointSlice()
			require.True(t, mp.groups[groupKey].toDistributionPoint(mp.labelKeysOrdered, &hdpL))
			require.Equal(t, 1, hdpL.Len(), "Exactly one point expected")
			got := hdpL.At(0)
			want := tt.want()
			require.Equal(t, want, got, "Expected the points to be equal")
		})
	}
}

func TestMetricGroupData_toDistributionPointEquivalence(t *testing.T) {
	type scrape struct {
		at     int64
		value  float64
		metric string
	}
	tests := []struct {
		name    string
		labels  labels.Labels
		scrapes []*scrape
	}{
		{
			name:   "histogram",
			labels: labels.Labels{{Name: "a", Value: "A"}, {Name: "le", Value: "0.75"}, {Name: "b", Value: "B"}},
			scrapes: []*scrape{
				{at: 11, value: 10, metric: "histogram_count"},
				{at: 11, value: 1004.78, metric: "histogram_sum"},
				{at: 13, value: 33.7, metric: "value"},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			mf := newMetricFamily(tt.name, mc, zap.NewNop()).(*metricFamily)
			mp := newMetricFamilyPdata(tt.name, mc).(*metricFamilyPdata)
			for _, tv := range tt.scrapes {
				require.NoError(t, mp.Add(tv.metric, tt.labels.Copy(), tv.at, tv.value))
				require.NoError(t, mf.Add(tv.metric, tt.labels.Copy(), tv.at, tv.value))
			}
			groupKey := mf.getGroupKey(tt.labels.Copy())
			ocTimeseries := mf.groups[groupKey].toDistributionTimeSeries(mf.labelKeysOrdered)
			hdpL := pdata.NewHistogramDataPointSlice()
			require.True(t, mp.groups[groupKey].toDistributionPoint(mp.labelKeysOrdered, &hdpL))
			require.Equal(t, len(ocTimeseries.Points), hdpL.Len(), "They should have the exact same number of points")
			require.Equal(t, 1, hdpL.Len(), "Exactly one point expected")
			ocPoint := ocTimeseries.Points[0]
			pdataPoint := hdpL.At(0)
			// 1. Ensure that the startTimestamps are equal.
			require.Equal(t, ocTimeseries.GetStartTimestamp().AsTime(), pdataPoint.Timestamp().AsTime(), "The timestamp must be equal")
			// 2. Ensure that the count is equal.
			ocHistogram := ocPoint.GetDistributionValue()
			require.Equal(t, ocHistogram.GetCount(), int64(pdataPoint.Count()), "Count must be equal")
			// 3. Ensure that the sum is equal.
			require.Equal(t, ocHistogram.GetSum(), pdataPoint.Sum(), "Sum must be equal")
			// 4. Ensure that the point's timestamp is equal to that from the OpenCensusProto data point.
			require.Equal(t, ocPoint.GetTimestamp().AsTime(), pdataPoint.Timestamp().AsTime(), "Point timestamps must be equal")
			// 5. Ensure that bucket bounds are the same.
			require.Equal(t, len(ocHistogram.GetBuckets()), len(pdataPoint.BucketCounts()), "Bucket counts must have the same length")
			var ocBucketCounts []uint64
			for i, bucket := range ocHistogram.GetBuckets() {
				ocBucketCounts = append(ocBucketCounts, uint64(bucket.GetCount()))

				// 6. Ensure that the exemplars match.
				ocExemplar := bucket.Exemplar
				if ocExemplar == nil {
					if i >= pdataPoint.Exemplars().Len() { // Both have the exact same number of exemplars.
						continue
					}
					// Otherwise an exemplar is present for the pdata data point but not for the OpenCensus Proto histogram.
					t.Fatalf("Exemplar #%d is ONLY present in the pdata point but not in the OpenCensus Proto histogram", i)
				}
				pdataExemplar := pdataPoint.Exemplars().At(i)
				msgPrefix := fmt.Sprintf("Exemplar #%d:: ", i)
				require.Equal(t, ocExemplar.Timestamp.AsTime(), pdataExemplar.Timestamp().AsTime(), msgPrefix+"timestamp mismatch")
				require.Equal(t, ocExemplar.Value, pdataExemplar.Value(), msgPrefix+"value mismatch")
				pdataExemplarAttachments := make(map[string]string)
				pdataExemplar.FilteredLabels().Range(func(key, value string) bool {
					pdataExemplarAttachments[key] = value
					return true
				})
				require.Equal(t, ocExemplar.Attachments, pdataExemplarAttachments, msgPrefix+"attachments mismatch")
			}
			// 7. Ensure that bucket bounds are the same.
			require.Equal(t, ocBucketCounts, pdataPoint.BucketCounts(), "Bucket counts must be equal")
			// 8. Ensure that the labels all match up.
			ocStringMap := pdata.NewStringMap()
			for i, labelValue := range ocTimeseries.LabelValues {
				ocStringMap.Insert(mf.labelKeysOrdered[i], labelValue.Value)
			}
			require.Equal(t, ocStringMap.Sort(), pdataPoint.LabelsMap().Sort())
		})
	}
}
