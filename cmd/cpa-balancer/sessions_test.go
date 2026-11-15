package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wappsdev/cpa-balancer/internal/engine"
	"github.com/wappsdev/cpa-balancer/internal/history"
)

func TestStatusCarriesTheSessionsOfTheLastFiveHoursAndWeek(t *testing.T) {
	dir := t.TempDir()
	// A slot from two days ago, already on disk when the plugin starts.
	old := time.Now().UTC().Add(-48 * time.Hour).Truncate(history.SlotLength)
	slot := fmt.Sprintf(`{"t":%q,"accounts":{"a.json":{"models":{"m":{"requests":1,"input_tokens":0,"output_tokens":0}}}},`+
		`"sessions":{"sess-old":{"first":%q,"last":%q,"accounts":{"a.json":{"requests":1,"input_tokens":0,"output_tokens":0}}}}}`+"\n",
		old.Format(time.RFC3339), old.Format(time.RFC3339), old.Format(time.RFC3339))
	if err := os.MkdirAll(filepath.Join(dir, "history"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "history", old.Format("2006-01-02")+".jsonl"), []byte(slot), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := recorder // the recorder outlives configurations, so this test brings its own
	recorder = history.NewRecorder(filepath.Join(dir, "history"))
	t.Cleanup(func() { recorder = previous })
	configureYAML(t, fmt.Sprintf("state_path: %s/state.json\naccounts: [{auth_file: a.json}]\nbuckets: {main: [m]}\n", dir))
	t.Cleanup(shutdown)
	recorder.AddTraffic(time.Now(), history.Source{Account: "a.json", Model: "m", Session: "sess-1"}, history.Traffic{Requests: 2, InputTokens: 10})

	v, ok := pluginAPI{}.Status().(engine.View)
	if !ok || v.Sessions == nil {
		t.Fatalf("status has no sessions: %+v", v)
	}
	if len(v.Sessions.FiveHour.Sessions) != 1 || v.Sessions.FiveHour.Total.Requests != 2 || v.Sessions.Week.Total.Requests != 3 {
		t.Fatalf("five hours: %+v\nweek: %+v", v.Sessions.FiveHour.Total, v.Sessions.Week.Total)
	}
}
