package tui

// QUM-769 — ChatRegion.View() cache.
//
// Even with the outer ChatList.Render cache, ChatRegion.View() still pays
// vp.SetContent + vp.View() (soft-wrap layout) every paint — ~220ms / 70MB
// over a 1500-envelope chat. This cache short-circuits the entire View()
// body when nothing observable to the rendered output has changed:
// chatlist revision, viewport size, scroll offset, autoScroll flag, and the
// hasNewContent indicator.
//
// Probe: ChatRegion exposes a private `viewBuilds` counter incremented
// inside the cache-miss branch of View().

import "testing"

func cachedTestChatRegion(width, height int) *ChatRegion {
	theme := NewTheme("")
	region := NewChatRegion(&theme)
	region.SetSize(width, height)
	region.ChatList().AppendUser("hello")
	region.ChatList().AppendUser("world")
	return region
}

func TestChatRegion_ViewCache_HitOnSecondCall(t *testing.T) {
	region := cachedTestChatRegion(80, 20)
	first := region.View()
	builds1 := region.viewBuilds
	second := region.View()
	builds2 := region.viewBuilds
	if first != second {
		t.Errorf("cached View() diverged from first call")
	}
	if builds2 != builds1 {
		t.Errorf("viewBuilds incremented on cache hit: %d -> %d", builds1, builds2)
	}
}

func TestChatRegion_ViewCache_InvalidatesOnChatListMutation(t *testing.T) {
	region := cachedTestChatRegion(80, 20)
	region.View()
	baseline := region.viewBuilds
	region.ChatList().AppendUser("another")
	region.View()
	if region.viewBuilds == baseline {
		t.Errorf("ChatList append did not invalidate ChatRegion cache")
	}
}

func TestChatRegion_ViewCache_InvalidatesOnSetSize(t *testing.T) {
	region := cachedTestChatRegion(80, 20)
	region.View()
	baseline := region.viewBuilds
	region.SetSize(60, 15)
	region.View()
	if region.viewBuilds == baseline {
		t.Errorf("SetSize did not invalidate ChatRegion cache")
	}
}

func TestChatRegion_ViewCache_InvalidatesOnScroll(t *testing.T) {
	region := cachedTestChatRegion(80, 5)
	for i := 0; i < 50; i++ {
		region.ChatList().AppendUser("filler")
	}
	region.View()
	yBefore := region.vp.YOffset()
	baseline := region.viewBuilds
	region.PageUp()
	region.View()
	if region.vp.YOffset() == yBefore {
		t.Fatalf("PageUp did not change yOffset (still %d); test fixture insufficient", yBefore)
	}
	if region.viewBuilds == baseline {
		t.Errorf("PageUp did not invalidate ChatRegion cache (yOffset moved %d -> %d)", yBefore, region.vp.YOffset())
	}
	builds := region.viewBuilds
	region.GotoBottom()
	region.View()
	if region.viewBuilds == builds {
		t.Errorf("GotoBottom did not invalidate ChatRegion cache")
	}
}

func TestChatRegion_ViewCache_OutputMatchesUncachedSteadyState(t *testing.T) {
	region := cachedTestChatRegion(80, 20)
	for i := 0; i < 10; i++ {
		region.ChatList().AppendUser("filler-line")
	}
	cached := region.View()

	// Build an identical region with cache disabled to obtain the oracle.
	theme := NewTheme("")
	oracle := NewChatRegion(&theme)
	oracle.disableViewCache = true
	oracle.SetSize(80, 20)
	oracle.ChatList().AppendUser("hello")
	oracle.ChatList().AppendUser("world")
	for i := 0; i < 10; i++ {
		oracle.ChatList().AppendUser("filler-line")
	}
	want := oracle.View()
	if cached != want {
		t.Errorf("cached View() differs from uncached oracle\n--- cached ---\n%s\n--- uncached ---\n%s", cached, want)
	}
}
