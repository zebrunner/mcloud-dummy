package android

import (
	"strings"
	"testing"
	"time"
)

func TestLogcatGetsTheAppEventsAfterTheEventLimit(t *testing.T) {
	d := testDevice(t)
	stopped := make(chan struct{})
	go func() {
		d.produceLogs(time.Millisecond)
		close(stopped)
	}()
	// waitLog waits for a line of logcat
	waitLog := func(text string) bool {
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			entries, _ := d.logs.since(0)
			for _, e := range entries {
				if strings.Contains(e.msg, text) {
					return true
				}
			}
		}
		return false
	}
	for i := range 250 {
		d.App.Log("test", "event", "n=%d", i)
	}
	if !waitLog("test event n=249") {
		t.Fatal("the events of the app are in logcat")
	}
	d.App.Log("test", "event", "late")
	if !waitLog("test event late") {
		t.Fatal("an event after the limit of the kept events of the app is in logcat")
	}
	d.Stop()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("the logs of a stopped device end")
	}
}
