package wikirender

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/template"
	"time"
)

type renderState struct {
	cfg            *RenderConfig
	projectPath    string // cached Store.ResolveProjectPath — resolved once, avoids sub-queries inside row loops
	learnings      map[int64]Learning
	learningsMu    sync.Mutex
	entities       map[string][]int64
	byLearning     map[int64][]string
	files          map[string]*FilePage
	scanFiles      map[string]*scanFile
	coupling       map[string][]CoEdit
	cooc           map[string][]CoTopic
	related        map[int64][]RelatedLearning
	sessions       map[string]Session
	contradictions []Contradiction
	gitignore      []gitignorePattern
	packageIntents map[string]string
	tpls           *template.Template
	computeMs      int64
	tplExecMs      int64
	skippedWrites  int
}

type learningView struct {
	Learning
	Related []RelatedLearning
}

type readmeView struct {
	Project         string
	BuiltAt         string
	LearningsCount  int
	TopicsCount     int
	PackagesCount   int
	FilesCount      int
	SessionsCount   int
	RecentSessions  []Session
}

type indexView struct {
	Project string
	Dirs    []indexDir
}

type indexDir struct {
	Name  string
	Files []*FilePage
}

type learningsIndexView struct {
	Project    string
	Categories []indexCategory
}

type indexCategory struct {
	Name      string
	Learnings []Learning
}

type healthView struct {
	Project          string
	BuiltAt          string
	LearningsCount   int
	QuarantinedCount int
	TopicsCount      int
	FilesCount       int
	SessionsCount    int
	Contradictions   []Contradiction
}

func newRenderState(cfg *RenderConfig) *renderState {
	var projectPath string
	if cfg != nil && cfg.Store != nil {
		projectPath = cfg.Store.ResolveProjectPath(cfg.Project)
	}
	return &renderState{
		cfg:            cfg,
		projectPath:    projectPath,
		learnings:      map[int64]Learning{},
		entities:       map[string][]int64{},
		byLearning:     map[int64][]string{},
		files:          map[string]*FilePage{},
		scanFiles:      map[string]*scanFile{},
		coupling:       map[string][]CoEdit{},
		cooc:           map[string][]CoTopic{},
		related:        map[int64][]RelatedLearning{},
		sessions:       map[string]Session{},
		contradictions: nil,
	}
}

func Render(ctx context.Context, cfg RenderConfig) (*Result, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	start := time.Now()
	rs := newRenderState(&cfg)
	loadStart := time.Now()
	if err := rs.loadAll(ctx); err != nil {
		return nil, err
	}
	loadMs := time.Since(loadStart).Milliseconds()

	rs.tpls = template.New("base").Funcs(tplFuncs())
	for name, body := range map[string]string{
		"learning":         learningTpl,
		"topic":            topicTpl,
		"file":             fileTpl,
		"session":          sessionTpl,
		"readme":           readmeTpl,
		"index":            indexTpl,
		"learnings_index":  learningsIndexTpl,
		"health":           healthTpl,
		"package":          packageTpl,
		"packages_index":   packagesIndexTpl,
	} {
		template.Must(rs.tpls.New(name).Parse(body))
	}

	writeStart := time.Now()
	if err := rs.writeAll(); err != nil {
		return nil, err
	}

	writeMs := time.Since(writeStart).Milliseconds()
	q := 0
	for _, l := range rs.learnings {
		if l.QuarantinedAt != "" {
			q++
		}
	}
	return &Result{
		Project:        cfg.Project,
		Learnings:      len(rs.learnings),
		Quarantined:    q,
		Topics:         rs.countTopics(),
		Files:          len(rs.files),
		Sessions:       len(rs.sessions),
		Contradictions: len(rs.contradictions),
		BuiltAt:        time.Now().Format(time.RFC3339),
		DurationMs:     time.Since(start).Milliseconds(),
		LoadMs:         loadMs,
		ComputeMs:      rs.computeMs,
		WriteMs:        writeMs,
		TplMs:          rs.tplExecMs,
		SkippedWrites:  rs.skippedWrites,
	}, nil
}

func (s *renderState) loadAll(ctx context.Context) error {
	// Phase 1: learnings must be loaded first — everything depends on them
	if err := s.loadLearnings(ctx); err != nil {
		return fmt.Errorf("learnings: %w", err)
	}

	// Pre-extract data from s.learnings (single-threaded, before any goroutine touches the map)
	supIDs := make([]int64, 0)
	sessIDs := make(map[string]bool)
	for _, l := range s.learnings {
		if l.Supersedes > 0 {
			supIDs = append(supIDs, l.Supersedes)
		}
		if l.SessionID != "" {
			sessIDs[l.SessionID] = true
		}
	}

	// Phase 2: run independent queries in parallel
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	var supersedesContent map[int64]string

	run := func(fn func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			}
		}()
	}

	run(func() error { return s.loadEntities(ctx) })
	run(func() error { return s.loadActions(ctx) })
	run(func() error { return s.loadKeywords(ctx) })
	run(func() error { return s.loadContradictions(ctx) })
	run(func() error { return s.loadFileCoverage(ctx) })
	run(func() error {
		m, err := s.loadSupersedesContentForIDs(ctx, supIDs)
		if err == nil && m != nil {
			mu.Lock()
			supersedesContent = m
			mu.Unlock()
		}
		return err
	})
	run(func() error {
		return s.loadSessionsForIDs(ctx, sessIDs)
	})

	wg.Wait()
	if firstErr != nil {
		return firstErr
	}

	// Phase 3: serial post-processing — needs data from all parallel goroutines
	s.learningsMu.Lock()
	if supersedesContent != nil {
		for id, l := range s.learnings {
			if l.Supersedes > 0 {
				if c, ok := supersedesContent[l.Supersedes]; ok {
					l.SupersedesContent = c
					s.learnings[id] = l
				}
			}
		}
	}
	s.learningsMu.Unlock()

	if err := s.loadScan(); err != nil {
		return err
	}
	s.mergeScanFiles()
	s.loadCLAUDEIntents()
	cStart := time.Now()
	s.computeCoOccurrence()
	s.computeRelatedLearnings()
	s.computeMs = time.Since(cStart).Milliseconds()
	s.linkLearningsToFiles()
	s.loadFileSessions(ctx)
	s.attachLearningsToSessions()
	return nil
}

func (s *renderState) writeAll() error {
	for _, d := range []string{"learnings", "topics", "files", "sessions", "packages"} {
		if err := os.MkdirAll(filepath.Join(s.cfg.OutputDir, d), 0755); err != nil {
			return err
		}
	}
	for _, l := range s.learnings {
		if err := s.writeLearning(l); err != nil {
			return err
		}
	}
	for name, lids := range s.entities {
		if len(lids) < 2 {
			continue
		}
		if err := s.writeTopic(name, lids); err != nil {
			return err
		}
	}
	for _, fp := range s.files {
		if err := s.writeFile(fp); err != nil {
			return err
		}
	}
	for _, sess := range s.sessions {
		if err := s.writeSession(sess); err != nil {
			return err
		}
	}
	if err := s.writePackages(); err != nil {
		return err
	}
	if err := s.writePackagesIndex(); err != nil {
		return err
	}
	if err := s.writeREADME(); err != nil {
		return err
	}
	if err := s.writeIndex(); err != nil {
		return err
	}
	if err := s.writeLearningsIndex(); err != nil {
		return err
	}
	if err := s.writeHealth(); err != nil {
		return err
	}
	return nil
}

func (s *renderState) writeLearning(l Learning) error {
	view := learningView{Learning: l, Related: s.related[l.ID]}
	path := filepath.Join(s.cfg.OutputDir, "learnings", fmt.Sprintf("%d.md", l.ID))
	return s.execTemplate(path, "learning", view)
}

func (s *renderState) writeTopic(name string, lids []int64) error {
	slug := slugify(name)
	if slug == "" {
		return nil
	}
	t := Topic{Name: name, CoTopics: s.cooc[name]}
	for _, id := range lids {
		t.Learnings = append(t.Learnings, s.learnings[id])
	}
	sort.Slice(t.Learnings, func(i, j int) bool { return t.Learnings[i].ID < t.Learnings[j].ID })
	return s.execTemplate(filepath.Join(s.cfg.OutputDir, "topics", slug+".md"), "topic", t)
}

func (s *renderState) writeFile(fp *FilePage) error {
	slug := fileSlug(fp.Path)
	if slug == "" {
		return nil
	}
	if c := s.lookupScan(fp.Path); c != nil {
		fp.Code = &FileCode{
			Language:   c.Language,
			LOC:        c.LOC,
			IsTest:     c.IsTest,
			TestCount:  c.TestCount,
			Signatures: c.Signatures,
			Imports:    c.Imports,
		}
	}
	fp.CoEdited = s.coupling[fp.Path]

	if s.cfg.CodeGraph != nil && strings.HasSuffix(fp.Path, ".go") {
		s.enrichFileGraph(fp)
	}

	return s.execTemplate(filepath.Join(s.cfg.OutputDir, "files", slug+".md"), "file", fp)
}

func (s *renderState) enrichFileGraph(fp *FilePage) {
	g := s.cfg.CodeGraph
	pkg := packageFromPath(fp.Path)
	if pkg == "" {
		return
	}
	fp.Package = pkg

	node := g.GetNode(pkg)
	if node == nil {
		return
	}

	// Imports: packages this file's package imports.
	seen := map[string]bool{}
	for _, e := range g.EdgesFrom(pkg) {
		if e.Kind != "imports" {
			continue
		}
		base := trimModulePrefix(e.To)
		if !seen[base] {
			seen[base] = true
			fp.Imports = append(fp.Imports, base)
		}
	}
	sort.Strings(fp.Imports)

	// Imported by: packages that import this file's package.
	seen = map[string]bool{}
	for _, e := range g.EdgesTo(pkg) {
		if e.Kind != "imports" {
			continue
		}
		base := trimModulePrefix(e.From)
		if !seen[base] {
			seen[base] = true
			fp.ImportedBy = append(fp.ImportedBy, base)
		}
	}
	sort.Strings(fp.ImportedBy)
}

func packageFromPath(path string) string {
	dir := filepath.Dir(path)
	if dir == "." {
		return "."
	}
	return dir
}

func trimModulePrefix(pkg string) string {
	pkg = strings.TrimPrefix(pkg, "github.com/carsteneu/yesmem/")
	return pkg
}

func (s *renderState) writeSession(sess Session) error {
	if sess.ShortID == "" {
		return nil
	}
	return s.execTemplate(filepath.Join(s.cfg.OutputDir, "sessions", sess.ShortID+".md"), "session", sess)
}

var nonPackageDirs = map[string]bool{
	"archiv": true, "skills": true, "scripts": true,
	"docs": true, "yesdocs": true, "caps-staging": true,
}

func (s *renderState) writePackages() error {
	// Group files by package directory.
	pkgFiles := map[string][]*FilePage{}
	for _, fp := range s.files {
		dir := fp.Directory
		if dir == "." || dir == "" {
			continue
		}
		pkgFiles[dir] = append(pkgFiles[dir], fp)
	}

	for pkg, files := range pkgFiles {
		// Only include packages with at least one Go file.
		hasGo := false
		for _, fp := range files {
			if fp.Code != nil && fp.Code.Language == "go" {
				hasGo = true
				break
			}
		}
		// Also include root-level (main) package.
		if !hasGo && pkg != "." {
			continue
		}
		// Exclude known non-package directories.
		if nonPackageDirs[pkg] || nonPackageDirs[filepath.Base(pkg)] {
			continue
		}

		pp := PackagePage{Name: pkg, FileCount: len(files)}
		pp.Intent = s.intentFor(pkg)

		// Aggregate LOC, language, test count, symbols from ALL files.
		symSeen := map[string]bool{}
		for _, fp := range files {
			if fp.Code != nil {
				if pp.Language == "" {
					pp.Language = fp.Code.Language
				}
				pp.TotalLOC += fp.Code.LOC
				if fp.Code.IsTest {
					pp.TestCount++
				}
				for _, s := range fp.Code.Signatures {
					if !symSeen[s] {
						symSeen[s] = true
						pp.Symbols = append(pp.Symbols, s)
					}
				}
			}
			if fp.LastTouched > pp.LastEdited {
				pp.LastEdited = fp.LastTouched
			}
		}
		sort.Strings(pp.Symbols)

		// Aggregate learnings (dedup by ID), gotchas, TODOs from ALL files.
		seen := map[int64]bool{}
		for _, fp := range files {
			for _, l := range fp.Learnings {
				if !seen[l.ID] {
					seen[l.ID] = true
					pp.Learnings = append(pp.Learnings, l)
				}
			}
		}
		for _, l := range pp.Learnings {
			if l.Category == "gotcha" {
				pp.Gotchas++
			}
			if l.TaskType == "task" || l.Category == "unfinished" || l.TaskType == "blocked" {
				pp.TODOs++
			}
		}
		sort.Slice(pp.Learnings, func(i, j int) bool { return pp.Learnings[i].ID < pp.Learnings[j].ID })

		// Top-20 files by session count for display only.
		display := make([]*FilePage, len(files))
		copy(display, files)
		sort.Slice(display, func(i, j int) bool { return display[i].SessionCount > display[j].SessionCount })
		if len(display) > 20 {
			display = display[:20]
		}
		pp.Files = make([]FilePage, len(display))
		for i, f := range display {
			pp.Files[i] = *f
		}

		// Derive sessions from learnings' session IDs (better metadata than file-level).
		sessSeen := map[string]bool{}
		for _, l := range pp.Learnings {
			if l.SessionID != "" && !sessSeen[l.SessionID] {
				sessSeen[l.SessionID] = true
				if sess, ok := s.sessions[l.SessionID]; ok {
					pp.Sessions = append(pp.Sessions, SessionRef{
						ID: sess.ShortID, StartedAt: sess.StartedAt, Messages: sess.MessageCount,
					})
				}
			}
		}
		sort.Slice(pp.Sessions, func(i, j int) bool { return pp.Sessions[i].StartedAt > pp.Sessions[j].StartedAt })
		if len(pp.Sessions) > 10 {
			pp.Sessions = pp.Sessions[:10]
		}

		if pp.LastEdited != "" {
			if t, err := time.Parse(time.RFC3339, pp.LastEdited); err == nil {
				pp.LastEdited = t.Format("2006-01-02")
			}
		}

		// Code-graph data: imports and dependents.
		if s.cfg.CodeGraph != nil {
			node := s.cfg.CodeGraph.GetNode(pkg)
			if node != nil {
				impSeen := map[string]bool{}
				for _, e := range s.cfg.CodeGraph.EdgesFrom(pkg) {
					if e.Kind != "imports" {
						continue
					}
					base := trimModulePrefix(e.To)
					if !impSeen[base] {
						impSeen[base] = true
						pp.Imports = append(pp.Imports, base)
					}
				}
				depSeen := map[string]bool{}
				for _, e := range s.cfg.CodeGraph.EdgesTo(pkg) {
					if e.Kind != "imports" {
						continue
					}
					base := trimModulePrefix(e.From)
					if !depSeen[base] {
						depSeen[base] = true
						pp.ImportedBy = append(pp.ImportedBy, base)
					}
				}
				sort.Strings(pp.Imports)
				sort.Strings(pp.ImportedBy)
			}
		}

		// Co-edited packages.
		pp.CoEdited = s.coupling[pkg]
		if len(pp.CoEdited) > 10 {
			pp.CoEdited = pp.CoEdited[:10]
		}

		slug := fileSlug(pkg)
		if slug == "" {
			continue
		}
		if err := s.execTemplate(filepath.Join(s.cfg.OutputDir, "packages", slug+".md"), "package", pp); err != nil {
			return err
		}
	}
	return nil
}

func (s *renderState) writePackagesIndex() error {
	// Build package list from the same aggregation used for writePackages.
	pkgFiles := map[string][]*FilePage{}
	for _, fp := range s.files {
		dir := fp.Directory
		if dir == "." || dir == "" {
			continue
		}
		pkgFiles[dir] = append(pkgFiles[dir], fp)
	}
	type pkgSummary struct {
		Name      string
		FileCount int
		TotalLOC  int
		Language  string
		Gotchas   int
		TODOs     int
	}
	var pkgs []pkgSummary
	for pkg, files := range pkgFiles {
		// Only Go packages (plus root-level main).
		hasGo := false
		for _, fp := range files {
			if fp.Code != nil && fp.Code.Language == "go" {
				hasGo = true
				break
			}
		}
		if !hasGo && pkg != "." {
			continue
		}

		ps := pkgSummary{Name: pkg, FileCount: len(files)}
		// Aggregate LOC from ALL files.
		for _, fp := range files {
			if fp.Code != nil {
				if ps.Language == "" {
					ps.Language = fp.Code.Language
				}
				ps.TotalLOC += fp.Code.LOC
			}
		}
		// Deduped learnings for gotcha/TODO counts.
		seen := map[int64]bool{}
		for _, fp := range files {
			for _, l := range fp.Learnings {
				if !seen[l.ID] {
					seen[l.ID] = true
					if l.Category == "gotcha" {
						ps.Gotchas++
					}
					if l.TaskType == "task" || l.Category == "unfinished" || l.TaskType == "blocked" {
						ps.TODOs++
					}
				}
			}
		}
		pkgs = append(pkgs, ps)
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Name < pkgs[j].Name })
	return s.execTemplate(filepath.Join(s.cfg.OutputDir, "packages.md"), "packages_index",
		map[string]any{"Project": s.cfg.Project, "Packages": pkgs})
}

func (s *renderState) writeREADME() error {
	recent := []Session{}
	for _, sess := range s.sessions {
		recent = append(recent, sess)
	}
	sort.Slice(recent, func(i, j int) bool { return recent[i].StartedAt > recent[j].StartedAt })
	if len(recent) > 10 {
		recent = recent[:10]
	}
	v := readmeView{
		Project:         s.cfg.Project,
		BuiltAt:         time.Now().Format(time.RFC3339),
		LearningsCount:  len(s.learnings),
		TopicsCount:     s.countTopics(),
		PackagesCount:   len(s.packageDirs()),
		FilesCount:      len(s.files),
		SessionsCount:   len(s.sessions),
		RecentSessions:  recent,
	}
	return s.execTemplate(filepath.Join(s.cfg.OutputDir, "README.md"), "readme", v)
}

func (s *renderState) writeIndex() error {
	dirMap := map[string][]*FilePage{}
	for _, fp := range s.files {
		dir := fp.Directory
		if dir == "" {
			dir = "."
		}
		dirMap[dir] = append(dirMap[dir], fp)
	}
	dirNames := []string{}
	for k := range dirMap {
		dirNames = append(dirNames, k)
	}
	sort.Strings(dirNames)
	var dirs []indexDir
	for _, name := range dirNames {
		files := dirMap[name]
		sort.Slice(files, func(i, j int) bool { return files[i].SessionCount > files[j].SessionCount })
		if len(files) > 50 {
			files = files[:50]
		}
		dirs = append(dirs, indexDir{Name: name, Files: files})
	}
	return s.execTemplate(filepath.Join(s.cfg.OutputDir, "index.md"), "index",
		indexView{Project: s.cfg.Project, Dirs: dirs})
}

func (s *renderState) writeLearningsIndex() error {
	catMap := map[string][]Learning{}
	for _, l := range s.learnings {
		cat := l.Category
		if cat == "" {
			cat = "uncategorized"
		}
		catMap[cat] = append(catMap[cat], l)
	}
	catNames := []string{}
	for k := range catMap {
		catNames = append(catNames, k)
	}
	sort.Slice(catNames, func(i, j int) bool {
		return len(catMap[catNames[i]]) > len(catMap[catNames[j]])
	})
	var cats []indexCategory
	for _, name := range catNames {
		ls := catMap[name]
		sort.Slice(ls, func(i, j int) bool { return ls[i].ID < ls[j].ID })
		cats = append(cats, indexCategory{Name: name, Learnings: ls})
	}
	return s.execTemplate(filepath.Join(s.cfg.OutputDir, "learnings.md"), "learnings_index",
		learningsIndexView{Project: s.cfg.Project, Categories: cats})
}

func (s *renderState) writeHealth() error {
	q := 0
	for _, l := range s.learnings {
		if l.QuarantinedAt != "" {
			q++
		}
	}
	v := healthView{
		Project:          s.cfg.Project,
		BuiltAt:          time.Now().Format(time.RFC3339),
		LearningsCount:   len(s.learnings),
		QuarantinedCount: q,
		TopicsCount:      s.countTopics(),
		FilesCount:       len(s.files),
		SessionsCount:    len(s.sessions),
		Contradictions:   s.contradictions,
	}
	return s.execTemplate(filepath.Join(s.cfg.OutputDir, "health.md"), "health", v)
}

func (s *renderState) execTemplate(path, name string, data any) error {
	tplStart := time.Now()
	var buf bytes.Buffer
	if err := s.tpls.ExecuteTemplate(&buf, name, data); err != nil {
		return err
	}
	s.tplExecMs += time.Since(tplStart).Milliseconds()
	rendered := buf.Bytes()

	existing, err := os.ReadFile(path)
	if err == nil && bytes.Equal(existing, rendered) {
		s.skippedWrites++
		return nil
	}

	return os.WriteFile(path, rendered, 0644)
}

func (s *renderState) countTopics() int {
	n := 0
	for _, lids := range s.entities {
		if len(lids) >= 2 {
			n++
		}
	}
	return n
}

func (s *renderState) packageDirs() []string {
	seen := map[string]bool{}
	for _, fp := range s.files {
		if fp.Directory != "." && fp.Directory != "" {
			seen[fp.Directory] = true
		}
	}
	dirs := make([]string, 0, len(seen))
	for d := range seen {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs
}
