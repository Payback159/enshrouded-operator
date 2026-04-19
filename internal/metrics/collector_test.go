/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package metrics_test

import (
	"testing"

	metrics "github.com/payback159/enshrouded-operator/internal/metrics"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestSetServerMetrics_MaintenanceWindowActive_True(t *testing.T) {
	metrics.SetServerMetrics("test-ns", "srv-active", 1, false, "Running", "", true)
	val := testutil.ToFloat64(metrics.MaintenanceWindowActive.WithLabelValues("test-ns", "srv-active"))
	if val != 1 {
		t.Errorf("expected MaintenanceWindowActive=1, got %v", val)
	}
}

func TestSetServerMetrics_MaintenanceWindowActive_False(t *testing.T) {
	metrics.SetServerMetrics("test-ns", "srv-inactive", 1, false, "Running", "", false)
	val := testutil.ToFloat64(metrics.MaintenanceWindowActive.WithLabelValues("test-ns", "srv-inactive"))
	if val != 0 {
		t.Errorf("expected MaintenanceWindowActive=0, got %v", val)
	}
}

func TestDeleteServerMetrics_ClearsMaintenanceWindowActive(t *testing.T) {
	metrics.SetServerMetrics("test-ns", "srv-todelete", 1, false, "Running", "", true)
	metrics.DeleteServerMetrics("test-ns", "srv-todelete", "Running")
	// After deletion the gauge falls back to zero when re-created via WithLabelValues.
	val := testutil.ToFloat64(metrics.MaintenanceWindowActive.WithLabelValues("test-ns", "srv-todelete"))
	if val != 0 {
		t.Errorf("expected MaintenanceWindowActive=0 after deletion, got %v", val)
	}
}
