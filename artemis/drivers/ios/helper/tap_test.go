package main

import (
	"context"
	"testing"
	"time"

	"github.com/danielpaulus/go-ios/ios/hid"
)

type recordingTouchPanel struct {
	contacts []hid.Point
	releases []hid.Point
}

func (p *recordingTouchPanel) TouchDown(point hid.Point) error {
	p.contacts = append(p.contacts, point)
	return nil
}

func (p *recordingTouchPanel) TouchUp(point hid.Point) error {
	p.releases = append(p.releases, point)
	return nil
}

func TestUpwardSwipeMovesOneContactBeforeReleasingIt(t *testing.T) {
	panel := &recordingTouchPanel{}
	from, to := hid.Point{X: 32000, Y: 48000}, hid.Point{X: 32000, Y: 16000}
	if err := performTouch(context.Background(), panel, from, to, 64*time.Millisecond, true); err != nil {
		t.Fatal(err)
	}
	positions := panel.contacts
	if len(positions) < 3 || positions[0] != from || positions[len(positions)-1] != to {
		t.Fatalf("swipe did not travel from %v to %v: %v", from, to, positions)
	}
	for i := 1; i < len(positions); i++ {
		if positions[i].X != from.X || positions[i].Y >= positions[i-1].Y {
			t.Fatalf("swipe changed direction: %v", positions)
		}
	}
	if len(panel.releases) != 1 || panel.releases[0] != to {
		t.Fatalf("expected one release at %v, got %v", to, panel.releases)
	}
}
