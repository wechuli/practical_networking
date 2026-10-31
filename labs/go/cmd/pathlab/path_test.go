//go:build linux

package main

import (
	"net/url"
	"testing"
	"time"
)

func TestParseRate(t *testing.T) {
	for in, want := range map[string]float64{"20mbit": 20e6, "1gbit": 1e9, "500kbit": 5e5, "0": 0, "1234": 1234} {
		if got, err := ParseRate(in); err != nil || got != want {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	if _, err := ParseRate("fast"); err == nil {
		t.Error("fast accepted")
	}
	if FormatRate(20e6) != "20Mbit" {
		t.Error(FormatRate(20e6))
	}
}

func TestApply(t *testing.T) {
	p, err := Params{Limit: 10}.Apply(url.Values{"delay": {"25ms"}, "loss": {"0.01"}, "rate": {"20mbit"}, "limit": {"30"}})
	if err != nil || p.Delay != 25*time.Millisecond || p.Loss != 0.01 || p.RateBps != 20e6 || p.Limit != 30 {
		t.Fatalf("%+v %v", p, err)
	}
	for _, bad := range []url.Values{{"loss": {"2"}}, {"colour": {"red"}}, {"delay": {"soon"}}} {
		if _, err := (Params{}).Apply(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestDelayOnly(t *testing.T) {
	path := NewPath(Params{Delay: 25 * time.Millisecond, Limit: 10})
	now := time.Now()
	at, ok := path.AB.admit(now, 1500)
	if !ok || at.Sub(now) != 25*time.Millisecond {
		t.Fatal(at.Sub(now), ok)
	}
}

func TestBottleneckSerialisesAndDrops(t *testing.T) {
	// 12 Mbit/s: a 1500-byte packet takes exactly 1 ms to send.
	path := NewPath(Params{RateBps: 12e6, Limit: 3})
	now := time.Now()
	var got []time.Duration
	for i := 0; i < 5; i++ {
		at, ok := path.AB.admit(now, 1500)
		if ok {
			got = append(got, at.Sub(now))
		}
	}
	want := []time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond}
	if len(got) != 3 || got[0] != want[0] || got[2] != want[2] {
		t.Fatalf("%v", got)
	}
	if s := path.AB.Stats(); s.QueueDrops != 2 || s.Packets != 5 {
		t.Fatalf("%+v", s)
	}
	// Once the queue has drained, packets are admitted again.
	if _, ok := path.AB.admit(now.Add(10*time.Millisecond), 1500); !ok {
		t.Fatal("queue did not drain")
	}
}

func TestRandomLoss(t *testing.T) {
	path := NewPath(Params{Loss: 0.1, Limit: 10})
	n := 20000
	for i := 0; i < n; i++ {
		path.BA.admit(time.Now(), 100)
	}
	got := float64(path.BA.Stats().LossDrops) / float64(n)
	if got < 0.08 || got > 0.12 {
		t.Fatalf("loss %.3f, want about 0.10", got)
	}
}

func TestDropBigIsOneShot(t *testing.T) {
	path := NewPath(Params{Limit: 10})
	path.SetParams(Params{Limit: 10, DropBig: 2})
	now := time.Now()
	var fates []bool
	if _, ok := path.AB.admit(now, 1500); !ok {
		t.Fatal("A→B packets are never targeted")
	}
	for _, size := range []int{1500, 60, 1500, 1500, 1500} {
		_, ok := path.BA.admit(now, size)
		fates = append(fates, ok)
	}
	want := []bool{true, true, false, true, true}
	for i := range want {
		if fates[i] != want[i] {
			t.Fatalf("fates %v, want %v (only the second large packet dropped)", fates, want)
		}
	}
	if path.BA.Stats().TargetedDrops != 1 || path.Params().DropBig != 0 {
		t.Fatalf("targeted drops %d, DropBig left at %d", path.BA.Stats().TargetedDrops, path.Params().DropBig)
	}
}
