package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bogem/id3v2/v2"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type state int

const (
	stateSelectFile state = iota
	stateViewMetadata
	stateSearchSelect
	stateManualInput
	stateLoading
)

type Metadata struct {
	Title    string
	Artist   string
	Album    string
	CoverArt []byte
}

type model struct {
	state         state
	prevState     state
	fileList      list.Model
	metaTable     table.Model
	resultsTable  table.Model
	inputs        []textinput.Model
	focusedInput  int
	selectedFile  string
	meta          Metadata
	searchResults []SearchResult
	statusMsg     string
	loadingText   string
}

type SearchResult struct {
	Title  string
	Artist string
	Album  string
	MBID   string // MusicBrainz Release ID
}

type fileItem struct {
	path string
}

func (i fileItem) Title() string       { return filepath.Base(i.path) }
func (i fileItem) Description() string { return i.path }
func (i fileItem) FilterValue() string { return i.path }

var baseStyle = lipgloss.NewStyle().
	BorderStyle(lipgloss.NormalBorder()).
	BorderForeground(lipgloss.Color("240")).
	Padding(1, 2)

var focusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

func initialModel() model {
	// Recursive MP3 discovery
	var items []list.Item
	_ = filepath.WalkDir(".", func(s string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.ToLower(filepath.Ext(s)) == ".mp3" {
			items = append(items, fileItem{path: s})
		}
		return nil
	})

	l := list.New(items, list.NewDefaultDelegate(), 40, 14)
	l.Title = "Select MP3 File"

	// Metadata Display Table
	mTable := table.New(
		table.WithColumns([]table.Column{
			{Title: "Field", Width: 18},
			{Title: "Value", Width: 45},
		}),
		table.WithFocused(false),
		table.WithHeight(5),
	)

	// Search Results Table
	rTable := table.New(
		table.WithColumns([]table.Column{
			{Title: "Title", Width: 25},
			{Title: "Artist", Width: 20},
			{Title: "Album", Width: 25},
		}),
		table.WithFocused(true),
		table.WithHeight(8),
	)

	tStyle := table.DefaultStyles()
	tStyle.Header = tStyle.Header.BorderStyle(lipgloss.NormalBorder()).Bold(true)
	tStyle.Selected = tStyle.Selected.Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57"))
	mTable.SetStyles(tStyle)
	rTable.SetStyles(tStyle)

	// Form Inputs for Manual Mode
	inputs := make([]textinput.Model, 3)
	for i := range inputs {
		t := textinput.New()
		t.CharLimit = 100
		switch i {
		case 0:
			t.Placeholder = "Title"
		case 1:
			t.Placeholder = "Artist"
		case 2:
			t.Placeholder = "Album"
		}
		inputs[i] = t
	}

	return model{
		state:        stateSelectFile,
		fileList:     l,
		metaTable:    mTable,
		resultsTable: rTable,
		inputs:       inputs,
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

// Msg types for async operations
type searchFinishedMsg struct {
	results []SearchResult
	err     error
}

type coverFetchedMsg struct {
	cover []byte
	err   error
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		}

		switch m.state {
		case stateSelectFile:
			switch msg.String() {
			case "enter":
				if i, ok := m.fileList.SelectedItem().(fileItem); ok {
					m.selectedFile = i.path
					m.loadMetadata()
					m.state = stateViewMetadata
				}
			case "q", "esc":
				return m, tea.Quit
			}

		case stateViewMetadata:
			switch msg.String() {
			case "q":
				return m, tea.Quit
			case "esc":
				return m, tea.Quit // Quit without saving
			case "f":
				m.state = stateLoading
				m.loadingText = "Searching online metadata..."
				return m, m.performOnlineSearch()
			case "m":
				m.state = stateManualInput
				m.inputs[0].SetValue(m.meta.Title)
				m.inputs[1].SetValue(m.meta.Artist)
				m.inputs[2].SetValue(m.meta.Album)
				m.focusedInput = 0
				m.inputs[0].Focus()
				return m, nil
			case "s":
				if err := m.saveMetadata(); err != nil {
					m.statusMsg = "Error saving: " + err.Error()
				} else {
					m.statusMsg = "Saved metadata & renamed file successfully!"
				}
			}

		case stateSearchSelect:
			switch msg.String() {
			case "enter":
				idx := m.resultsTable.Cursor()
				if idx >= 0 && idx < len(m.searchResults) {
					res := m.searchResults[idx]
					m.meta.Title = res.Title
					m.meta.Artist = res.Artist
					m.meta.Album = res.Album
					m.updateTableRows()

					if res.MBID != "" {
						m.state = stateLoading
						m.loadingText = "Fetching cover art..."
						return m, m.fetchCoverArt(res.MBID)
					}
					m.state = stateViewMetadata
				}
			case "esc":
				m.state = stateViewMetadata
			}

		case stateManualInput:
			switch msg.String() {
			case "enter":
				if m.focusedInput < len(m.inputs)-1 {
					m.inputs[m.focusedInput].Blur()
					m.focusedInput++
					m.inputs[m.focusedInput].Focus()
				} else {
					m.meta.Title = m.inputs[0].Value()
					m.meta.Artist = m.inputs[1].Value()
					m.meta.Album = m.inputs[2].Value()
					m.updateTableRows()
					m.state = stateViewMetadata
				}
				return m, nil
			case "tab":
				m.inputs[m.focusedInput].Blur()
				m.focusedInput = (m.focusedInput + 1) % len(m.inputs)
				m.inputs[m.focusedInput].Focus()
				return m, nil
			case "esc":
				m.state = stateViewMetadata
				return m, nil
			}
		}

	case searchFinishedMsg:
		if msg.err != nil {
			m.statusMsg = "Search failed: " + msg.err.Error()
			m.state = stateViewMetadata
			return m, nil
		}
		if len(msg.results) == 0 {
			m.statusMsg = "No metadata found online."
			m.state = stateViewMetadata
			return m, nil
		}
		m.searchResults = msg.results
		var rows []table.Row
		for _, r := range msg.results {
			rows = append(rows, table.Row{r.Title, r.Artist, r.Album})
		}
		m.resultsTable.SetRows(rows)
		m.state = stateSearchSelect
		return m, nil

	case coverFetchedMsg:
		if msg.err == nil {
			m.meta.CoverArt = msg.cover
			m.statusMsg = "Online metadata & cover art loaded!"
		} else {
			m.statusMsg = "Metadata loaded (Cover art not found)."
		}
		m.updateTableRows()
		m.state = stateViewMetadata
		return m, nil
	}

	// Delegate updates based on state
	switch m.state {
	case stateSelectFile:
		m.fileList, cmd = m.fileList.Update(msg)
	case stateSearchSelect:
		m.resultsTable, cmd = m.resultsTable.Update(msg)
	case stateManualInput:
		cmd = m.updateInputs(msg)
	}

	return m, cmd
}

func (m *model) updateInputs(msg tea.Msg) tea.Cmd {
	cmds := make([]tea.Cmd, len(m.inputs))
	for i := range m.inputs {
		m.inputs[i], cmds[i] = m.inputs[i].Update(msg)
	}
	return tea.Batch(cmds...)
}

func (m *model) loadMetadata() {
	tag, err := id3v2.Open(m.selectedFile, id3v2.Options{Parse: true})
	if err != nil {
		m.meta = Metadata{}
		m.updateTableRows()
		return
	}
	defer tag.Close()

	var cover []byte
	frames := tag.GetFrames(tag.CommonID("Attached picture"))
	if len(frames) > 0 {
		if pic, ok := frames[0].(id3v2.PictureFrame); ok {
			cover = pic.Picture
		}
	}

	m.meta = Metadata{
		Title:    tag.Title(),
		Artist:   tag.Artist(),
		Album:    tag.Album(),
		CoverArt: cover,
	}
	m.updateTableRows()
}

func (m *model) updateTableRows() {
	hasCover := "No"
	if len(m.meta.CoverArt) > 0 {
		hasCover = fmt.Sprintf("Yes (%d bytes)", len(m.meta.CoverArt))
	}

	rows := []table.Row{
		{"Title", m.meta.Title},
		{"Artist Name", m.meta.Artist},
		{"Album Name", m.meta.Album},
		{"Album Cover Art", hasCover},
	}
	m.metaTable.SetRows(rows)
}

func (m *model) saveMetadata() error {
	tag, err := id3v2.Open(m.selectedFile, id3v2.Options{Parse: true})
	if err != nil {
		return err
	}
	defer tag.Close()

	tag.SetTitle(m.meta.Title)
	tag.SetArtist(m.meta.Artist)
	tag.SetAlbum(m.meta.Album)

	if len(m.meta.CoverArt) > 0 {
		pic := id3v2.PictureFrame{
			Encoding:    id3v2.EncodingUTF8,
			MimeType:    "image/jpeg",
			PictureType: id3v2.PTFrontCover,
			Description: "Front Cover",
			Picture:     m.meta.CoverArt,
		}
		tag.AddAttachedPicture(pic)
	}

	if err = tag.Save(); err != nil {
		return err
	}

	// Auto-rename format: "<title> (artist).mp3"
	if m.meta.Title != "" && m.meta.Artist != "" {
		dir := filepath.Dir(m.selectedFile)
		safeTitle := sanitizeFilename(m.meta.Title)
		safeArtist := sanitizeFilename(m.meta.Artist)
		newName := fmt.Sprintf("%s (%s).mp3", safeTitle, safeArtist)
		newPath := filepath.Join(dir, newName)

		if newPath != m.selectedFile {
			if err := os.Rename(m.selectedFile, newPath); err != nil {
				return err
			}
			m.selectedFile = newPath
		}
	}
	return nil
}

func sanitizeFilename(s string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	return r.Replace(s)
}

// API Integration for Online Metadata Search
func (m model) performOnlineSearch() tea.Cmd {
	return func() tea.Msg {
		searchTerm := m.meta.Title
		if searchTerm == "" {
			searchTerm = strings.TrimSuffix(filepath.Base(m.selectedFile), filepath.Ext(m.selectedFile))
		}

		client := &http.Client{Timeout: 8 * time.Second}
		query := url.QueryEscape(searchTerm)
		reqURL := fmt.Sprintf("https://musicbrainz.org/ws/2/recording?query=%s&fmt=json&limit=5", query)

		req, _ := http.NewRequest("GET", reqURL, nil)
		req.Header.Set("User-Agent", "TaggerCLI/1.0 (contact@example.com)")

		resp, err := client.Do(req)
		if err != nil {
			return searchFinishedMsg{err: err}
		}
		defer resp.Body.Close()

		var mbResp struct {
			Recordings []struct {
				Title        string `json:"title"`
				ArtistCredit []struct {
					Name string `json:"name"`
				} `json:"artist-credit"`
				Releases []struct {
					ID    string `json:"id"`
					Title string `json:"title"`
				} `json:"releases"`
			} `json:"recordings"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&mbResp); err != nil {
			return searchFinishedMsg{err: err}
		}

		var results []SearchResult
		for _, rec := range mbResp.Recordings {
			artist := ""
			if len(rec.ArtistCredit) > 0 {
				artist = rec.ArtistCredit[0].Name
			}
			album := ""
			mbid := ""
			if len(rec.Releases) > 0 {
				album = rec.Releases[0].Title
				mbid = rec.Releases[0].ID
			}

			results = append(results, SearchResult{
				Title:  rec.Title,
				Artist: artist,
				Album:  album,
				MBID:   mbid,
			})
		}

		return searchFinishedMsg{results: results}
	}
}

func (m model) fetchCoverArt(releaseID string) tea.Cmd {
	return func() tea.Msg {
		reqURL := fmt.Sprintf("https://coverartarchive.org/release/%s/front", releaseID)
		client := &http.Client{Timeout: 8 * time.Second}

		resp, err := client.Get(reqURL)
		if err != nil || resp.StatusCode != http.StatusOK {
			return coverFetchedMsg{err: fmt.Errorf("cover art unavailable")}
		}
		defer resp.Body.Close()

		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return coverFetchedMsg{err: err}
		}
		return coverFetchedMsg{cover: data}
	}
}

func (m model) View() string {
	switch m.state {
	case stateSelectFile:
		return baseStyle.Render(m.fileList.View())

	case stateLoading:
		return baseStyle.Render(fmt.Sprintf("\n  ⌛ %s\n", m.loadingText))

	case stateViewMetadata:
		s := fmt.Sprintf("File: %s\n\n", m.selectedFile)
		s += m.metaTable.View() + "\n\n"
		s += "[f] Find Online  |  [m] Enter Manually  |  [s] Save Metadata  |  [q/Esc] Quit\n"
		if m.statusMsg != "" {
			s += "\n" + focusStyle.Render(m.statusMsg) + "\n"
		}
		return baseStyle.Render(s)

	case stateSearchSelect:
		s := "Select online match:\n\n"
		s += m.resultsTable.View() + "\n\n"
		s += "[Enter] Select  |  [Esc] Cancel\n"
		return baseStyle.Render(s)

	case stateManualInput:
		s := "Enter Metadata Manually:\n\n"
		s += fmt.Sprintf("Title:       %s\n", m.inputs[0].View())
		s += fmt.Sprintf("Artist Name: %s\n", m.inputs[1].View())
		s += fmt.Sprintf("Album Name:  %s\n\n", m.inputs[2].View())
		s += "[Enter/Tab] Next field/Confirm  |  [Esc] Cancel\n"
		return baseStyle.Render(s)
	}
	return ""
}

func main() {
	p := tea.NewProgram(initialModel())
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running Tagger: %v\n", err)
		os.Exit(1)
	}
}
