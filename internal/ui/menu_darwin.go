package ui

import "github.com/ebitengine/purego/objc"

// fixAppMenu removes the cmd+h (hide), alt+cmd+h and cmd+m (minimize to
// the Dock) shortcuts from the menu created by GLFW: without this, cmd+h
// would hide the app instead of moving focus vim-style, and cmd+m would
// minimize it instead of sending the pane to the garage.
// Called from Update, which runs on the main thread (SingleThread mode).
func fixAppMenu() {
	app := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	menu := app.Send(objc.RegisterName("mainMenu"))
	if menu == 0 {
		return
	}
	empty := objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("alloc")).
		Send(objc.RegisterName("initWithUTF8String:"), "\x00")
	blocked := map[objc.SEL]bool{
		objc.RegisterName("hide:"):                  true,
		objc.RegisterName("hideOtherApplications:"): true,
		objc.RegisterName("performMiniaturize:"):    true,
	}
	selItem := objc.RegisterName("itemAtIndex:")
	n := int(menu.Send(objc.RegisterName("numberOfItems")))
	for i := 0; i < n; i++ {
		sub := menu.Send(selItem, i).Send(objc.RegisterName("submenu"))
		if sub == 0 {
			continue
		}
		m := int(sub.Send(objc.RegisterName("numberOfItems")))
		for j := 0; j < m; j++ {
			it := sub.Send(selItem, j)
			if blocked[objc.SEL(it.Send(objc.RegisterName("action")))] {
				it.Send(objc.RegisterName("setKeyEquivalent:"), empty)
			}
		}
	}
}
