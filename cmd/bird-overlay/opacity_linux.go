//go:build desktop && linux

package main

/*
#cgo pkg-config: gtk+-3.0
#include <gtk/gtk.h>

static gboolean bird_set_opacity(gpointer p) {
	double o = GPOINTER_TO_INT(p) / 100.0;
	GList *wins = gtk_window_list_toplevels();
	for (GList *l = wins; l != NULL; l = l->next) {
		if (gtk_window_get_window_type(GTK_WINDOW(l->data)) == GTK_WINDOW_TOPLEVEL)
			gtk_widget_set_opacity(GTK_WIDGET(l->data), o);
	}
	g_list_free(wins);
	return G_SOURCE_REMOVE;
}

static void bird_queue_opacity(int percent) {
	g_idle_add(bird_set_opacity, GINT_TO_POINTER(percent));
}
*/
import "C"

// setWindowOpacity sets the overlay window's opacity (0..1) on the GTK main
// thread. Queued in order with Wails' own main-thread calls (e.g. show).
func setWindowOpacity(o float64) {
	C.bird_queue_opacity(C.int(o * 100))
}
