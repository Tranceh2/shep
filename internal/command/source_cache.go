package command

import (
	"context"

	"github.com/tranceh2/shep/internal/config"
	"github.com/tranceh2/shep/internal/pathutil"
	"github.com/tranceh2/shep/internal/sourcecache"
	"github.com/tranceh2/shep/internal/tui"
)

// sourceCacheLocation is where a slow source's last result is saved and the
// fingerprint of the configuration it was produced under.
type sourceCacheLocation struct {
	dir, fingerprint string
}

// sourceCache locates the saved result of a slow source: the projects scan
// reads directories and a custom source runs a command, so the picker shows
// their last result at once while they run again. ok is false for every
// other source, which answers fast or must be live (Herdr).
func (a *App) sourceCache(name string) (sourceCacheLocation, bool) {
	cfg := a.Config()
	var fingerprint string
	if name == config.SourceProjects {
		fingerprint = sourcecache.Fingerprint(cfg.Sources.Projects)
	} else {
		for _, custom := range cfg.Sources.Custom {
			if custom.Name == name {
				fingerprint = sourcecache.Fingerprint(custom)
				break
			}
		}
	}
	if fingerprint == "" {
		return sourceCacheLocation{}, false
	}
	dir, err := pathutil.CachePath("shep", "sources")
	if err != nil {
		return sourceCacheLocation{}, false
	}
	return sourceCacheLocation{dir: dir, fingerprint: fingerprint}, true
}

// buildCachedProducer streams a slow source's last saved result, marked
// Cached so the picker drops it when the source's fresh result came first.
// It is nil for a source without a saved result location.
func (a *App) buildCachedProducer(name string) tui.SourceProducer {
	cache, ok := a.sourceCache(name)
	if !ok {
		return nil
	}
	settings := a.settings()
	return func(context.Context) tui.SourceResultMsg {
		saved, ok := sourcecache.Load(cache.dir, name, cache.fingerprint)
		if !ok {
			return tui.SourceResultMsg{Source: name, Cached: true}
		}
		settings.Attach(saved.Candidates)
		return tui.SourceResultMsg{Source: name, Candidates: saved.Candidates, NormalizedPaths: saved.NormalizedPaths, Cached: true}
	}
}
