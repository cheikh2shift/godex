#include <gtk/gtk.h>
#include <vte/vte.h>
#include <stdlib.h>

static char *build_exec_path(void) {
	const char *snap = g_getenv("SNAP");
	if (snap && *snap) {
		return g_strconcat(snap, "/bin/godex", NULL);
	}
	return g_strdup("godex");
}

int main(int argc, char **argv) {
	gtk_init(&argc, &argv);

	GtkWidget *window = gtk_window_new(GTK_WINDOW_TOPLEVEL);
	gtk_window_set_title(GTK_WINDOW(window), "Godex");
	gtk_window_set_default_size(GTK_WINDOW(window), 960, 600);
	g_signal_connect(window, "destroy", G_CALLBACK(gtk_main_quit), NULL);

	VteTerminal *terminal = VTE_TERMINAL(vte_terminal_new());
	PangoFontDescription *font = pango_font_description_from_string("Monospace 12");
	vte_terminal_set_font(terminal, font);
	pango_font_description_free(font);

	GdkRGBA fg = {0};
	GdkRGBA bg = {0};
	gdk_rgba_parse(&fg, "#E6E6E6");
	gdk_rgba_parse(&bg, "#0F1115");
	vte_terminal_set_colors(terminal, &fg, &bg, NULL, 0);
	vte_terminal_set_cursor_blink_mode(terminal, VTE_CURSOR_BLINK_ON);
	vte_terminal_set_cursor_shape(terminal, VTE_CURSOR_SHAPE_BLOCK);
	vte_terminal_set_scrollback_lines(terminal, 10000);
	vte_terminal_set_bold_is_bright(terminal, TRUE);

	gtk_container_add(GTK_CONTAINER(window), GTK_WIDGET(terminal));
	gtk_widget_show_all(window);

	char *exec_path = build_exec_path();
	char *child_argv[] = {exec_path, NULL};

	vte_terminal_spawn_async(
		terminal,
		VTE_PTY_DEFAULT,
		NULL,
		child_argv,
		NULL,
		G_SPAWN_DEFAULT,
		NULL,
		NULL,
		NULL,
		-1,
		NULL,
		NULL,
		NULL);

	g_free(exec_path);
	gtk_main();
	return 0;
}
