package web

import (
	"bytes"
	"fmt"
	"html/template"
	htmxgolangexcercise "htmx-golang-excercise"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
)

// StaticHandler serves the embedded assets under /static/ (JS, CSS, ...).
var StaticHandler = buildStaticHandler()

func buildStaticHandler() http.Handler {
	sub, err := fs.Sub(htmxgolangexcercise.StaticFiles, "static")
	if err != nil {
		log.Fatalf("Failed to prepare static assets: %v", err)
	}
	return http.StripPrefix("/static/", http.FileServer(http.FS(sub)))
}

type TemplateCache map[string]*template.Template

var templates TemplateCache

func InitTemplates() error {
	cache := make(TemplateCache)

	funcMap := template.FuncMap{
		"add": func(a, b int) int { return a + b },
	}

	pages, err := fs.Glob(htmxgolangexcercise.Files, "templates/pages/*.html")
	if err != nil {
		return err
	}

	for _, page := range pages {
		name := filepath.Base(page)

		templ, err := template.New(name).Funcs(funcMap).ParseFS(
			htmxgolangexcercise.Files,
			"templates/layouts/*.html",
			"templates/partials/*.html",
			page,
		)

		if err != nil {
			return fmt.Errorf("error parsing page %s: %w", name, err)
		}

		cache[name] = templ
	}

	partials, err := fs.Glob(htmxgolangexcercise.Files, "templates/partials/*.html")
	if err != nil {
		return err
	}

	for _, partial := range partials {
		name := filepath.Base(partial)

		tmpl, err := template.New(name).Funcs(funcMap).ParseFS(htmxgolangexcercise.Files, partial)
		if err != nil {
			return fmt.Errorf("error parsing partial %s: %w", name, err)
		}

		cache["partial:"+name] = tmpl
	}

	templates = cache
	return nil
}

func Render(w http.ResponseWriter, name string, data any) {
	tmpl, ok := templates[name]
	if !ok {
		log.Printf("Render error: template %s not found", name)
		http.Error(w, fmt.Sprintf("Template %s not found", name), http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		log.Printf("Render error [%s]: %v", name, err)
		http.Error(w, "Failed to render page", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(buf.Bytes()); err != nil {
		log.Printf("Render write error [%s]: %v", name, err)
	}
}

func RenderPartial(w http.ResponseWriter, name string, data any) {
	tmpl, ok := templates["partial:"+name]
	if !ok {
		log.Printf("RenderPartial error: template partial:%s not found", name)
		http.Error(w, fmt.Sprintf("Partial template %s not found", name), http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		log.Printf("RenderPartial error [%s]: %v", name, err)
		http.Error(w, "Failed to render fragment", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(buf.Bytes()); err != nil {
		log.Printf("RenderPartial write error [%s]: %v", name, err)
	}
}
