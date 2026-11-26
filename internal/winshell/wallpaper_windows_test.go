package winshell

import "testing"

func TestCurrentWallpaperDoesNotCrash(t *testing.T) {
	state, err := CurrentWallpaper()
	t.Logf("CurrentWallpaper: err=%v pos=%d bg=%+v monitors=%+v", err, state.Position, state.Background, state.Monitors)
	if state.Background.A != 255 {
		t.Fatalf("expected opaque background fallback color, got %+v", state.Background)
	}
}
