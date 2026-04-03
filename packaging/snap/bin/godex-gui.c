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
