package livesource

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Directory answers "where do I look in my city" with a real destination.
//
// It is not a listing feed and does not pretend to be: it returns the official
// public-employment-service site for the person's region plus the two nationwide
// hotlines. That is the honest floor — every city in the country has a public
// employment service, it is free, and it is where the named employers actually
// are. Sending somebody there beats both an invented listing and an apology.
//
// Every URL in the data file was fetched and answered on its verified_at date;
// regions whose host did not respond were left out rather than guessed.
type Directory struct {
	entries  map[string]dirEntry
	alias    map[string]string
	inRegion map[string]string
	note     string
}

type dirEntry struct {
	Region     string `json:"region"`
	Site       string `json:"official_site"`
	HotlineHR  string `json:"hotline_hr"`
	HotlineGov string `json:"hotline_gov"`
	Verified   string `json:"verified_at"`
}

type dirDoc struct {
	Note         string            `json:"note"`
	Aliases      map[string]string `json:"aliases"`
	CityInRegion map[string]string `json:"city_in_region"`
	Entries      []dirEntry        `json:"entries"`
}

func LoadDirectory(dir string) (*Directory, error) {
	path := filepath.Join(dir, "service_directory.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("DIRECTORY_READ_FAILED: cannot read %s: %w", path, err)
	}
	var doc dirDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("DIRECTORY_PARSE_FAILED: %s: %w", path, err)
	}
	d := &Directory{
		entries: make(map[string]dirEntry, len(doc.Entries)),
		alias:   doc.Aliases, inRegion: doc.CityInRegion, note: doc.Note,
	}
	for _, e := range doc.Entries {
		if e.Site == "" || e.Region == "" {
			return nil, fmt.Errorf("DIRECTORY_INVALID: entry %q has no region or site", e.Region)
		}
		d.entries[e.Region] = e
	}
	return d, nil
}

func (d *Directory) Name() string { return "directory" }

func (d *Directory) Regions() int { return len(d.entries) }

// resolve maps whatever the person typed onto a region that has an entry:
// the city itself, then its alias, then the province that runs its public
// employment service.
func (d *Directory) resolve(city string) (dirEntry, bool) {
	city = strings.TrimSpace(city)
	if city == "" {
		return dirEntry{}, false
	}
	for _, key := range []string{city, d.alias[city], d.inRegion[city], d.alias[d.inRegion[city]]} {
		if key == "" {
			continue
		}
		if e, ok := d.entries[key]; ok {
			return e, true
		}
	}
	return dirEntry{}, false
}

func (d *Directory) Lookup(ctx context.Context, q Query) ([]Result, error) {
	e, ok := d.resolve(q.City)
	if !ok {
		// No entry is not a failure. 12333 and 12345 are nationwide short codes
		// that reach the caller's own city, so there is always a real answer —
		// it just has no URL attached, and says so.
		if strings.TrimSpace(q.City) == "" {
			return nil, nil
		}
		return []Result{{
			Kind: KindDirectory, Region: q.City,
			Title:   q.City + "：公共就业服务（全国免费）",
			Summary: "每个城市都有公共就业服务机构，免费提供岗位信息、职业介绍和失业登记，不看户籍。打 12333 问就近网点。",
			Phone:   "12333", Source: "人社部 12333 全国统一热线",
			Caveat: "这里没有收录 " + q.City + " 人社部门的网址，所以不给网址；12333 是全国统一号码，" +
				"在 " + q.City + " 拨通的就是 " + q.City + " 的话务中心。",
		}}, nil
	}
	return []Result{{
		Kind: KindDirectory, Region: e.Region,
		Title:   e.Region + "人力资源和社会保障部门（官方）",
		Summary: "本地公共就业服务的官方入口：岗位信息、职业介绍、培训目录、补贴申报都在这里发布和受理。",
		URL:     e.Site, Phone: e.HotlineHR,
		Source: "官方站点", Verified: e.Verified,
		Caveat: "这是官方入口，不是具体岗位。具体在招的单位和课程以该站点当前发布的为准。",
	}}, nil
}
