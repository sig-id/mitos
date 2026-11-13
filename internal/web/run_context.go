package web

import "strings"

type runLog struct {
	Name, Text, Note string
}

// Show the recorded launch source without guessing an individual identity.
func (s *Server) runContext(p *page) {
	if p.Run.Sample {
		p.RunSource = "Built-in demo history (--demo); no agent was launched"
		p.RunProjectName = "None – illustrative workspace"
		return
	}
	p.RunSource = "Not recorded"
	switch p.Task.Source {
	case "human":
		p.RunSource = "Manual request; individual identity was not recorded"
	case "graph":
		p.RunSource = "Workflow coordinator"
	default:
		if strings.HasPrefix(p.Task.Source, "trigger:") {
			p.RunSource = "Service trigger: " + strings.TrimPrefix(p.Task.Source, "trigger:")
		}
	}
	p.RunProjectName = "No project assigned"
	if id := p.Run.Brief.ProjectID; id != "" {
		p.RunProjectName = id
		if project, err := s.engine.Desk.Project(id); err == nil {
			p.RunProjectName = project.Name
		}
	}
	for _, artifact := range p.Run.Artifacts {
		if artifact.Name != "report.md" && artifact.Name != "output.txt" && artifact.Name != "acceptance.txt" && !(strings.HasPrefix(artifact.Name, "segment-") && (strings.HasSuffix(artifact.Name, "-output.txt") || strings.HasSuffix(artifact.Name, "-acceptance.txt"))) {
			continue
		}
		log := runLog{Name: artifact.Name}
		if artifact.Bytes > 1<<20 {
			log.Note = "This log is too large for an inline preview. Download it to read the full contents."
		} else if data, err := s.engine.Store.Artifact(p.Run.ID, artifact.Name); err != nil {
			log.Note = "The saved log could not be read: " + err.Error()
		} else {
			if len(data) > 64<<10 {
				data = data[len(data)-(64<<10):]
				log.Note = "Showing the last 64 KiB. Download the log for the full contents."
			}
			log.Text = strings.ToValidUTF8(string(data), "�")
		}
		p.RunLogs = append(p.RunLogs, log)
	}
}
