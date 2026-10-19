package models

import (
	"encoding/json"
	"errors"
	"sort"

	"github.com/google/uuid"
)

var ErrAITaskOptions = errors.New("choose readonly, standard or automatic approval, at most 8 skills totaling 96 KiB and 4 plugins, without duplicate IDs")

type AITaskSelection struct {
	ApprovalMode string      `json:"approval_mode"`
	SkillIDs     []uuid.UUID `json:"skill_ids"`
	PluginIDs    []uuid.UUID `json:"plugin_ids"`
}

func (o *AITaskSelection) Normalize() error {
	if o.ApprovalMode == "" {
		o.ApprovalMode = "standard"
	}
	if o.ApprovalMode != "readonly" && o.ApprovalMode != "standard" && o.ApprovalMode != "automatic" || len(o.SkillIDs) > 8 || len(o.PluginIDs) > 4 {
		return ErrAITaskOptions
	}
	for _, ids := range [][]uuid.UUID{o.SkillIDs, o.PluginIDs} {
		seen := map[uuid.UUID]bool{}
		for _, id := range ids {
			if id == uuid.Nil || seen[id] {
				return ErrAITaskOptions
			}
			seen[id] = true
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	}
	return nil
}

type AITaskExtensionRef struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}

type AITaskSkillSnapshot struct {
	AITaskExtensionRef
	Content string `json:"content"`
}

type AITaskPluginSnapshot struct {
	AITaskExtensionRef
	URL         string `json:"url"`
	Fingerprint string `json:"fingerprint"`
}

// Snapshot instruction text at creation. Plugin secrets remain encrypted in
// their own table: only their configuration fingerprint is copied here.
type AITaskOptions struct {
	Skills  []AITaskSkillSnapshot  `json:"skills"`
	Plugins []AITaskPluginSnapshot `json:"plugins"`
}

func (t *AITask) DecodeOptions() (AITaskOptions, error) {
	var options AITaskOptions
	if len(t.Options) > 0 {
		if err := json.Unmarshal(t.Options, &options); err != nil {
			return options, err
		}
	}
	t.Skills = []AITaskExtensionRef{}
	t.Plugins = []AITaskExtensionRef{}
	for _, skill := range options.Skills {
		t.Skills = append(t.Skills, skill.AITaskExtensionRef)
	}
	for _, plugin := range options.Plugins {
		t.Plugins = append(t.Plugins, plugin.AITaskExtensionRef)
	}
	return options, nil
}

func (t AITask) MatchesSelection(selection AITaskSelection) bool {
	if selection.Normalize() != nil || t.ApprovalMode != selection.ApprovalMode {
		return false
	}
	options, err := t.DecodeOptions()
	if err != nil || len(options.Skills) != len(selection.SkillIDs) || len(options.Plugins) != len(selection.PluginIDs) {
		return false
	}
	for i, skill := range options.Skills {
		if skill.ID != selection.SkillIDs[i] {
			return false
		}
	}
	for i, plugin := range options.Plugins {
		if plugin.ID != selection.PluginIDs[i] {
			return false
		}
	}
	return true
}

func (t AITask) Selection() (AITaskSelection, error) {
	selection := AITaskSelection{ApprovalMode: t.ApprovalMode}
	options, err := t.DecodeOptions()
	if err != nil {
		return selection, err
	}
	for _, skill := range options.Skills {
		selection.SkillIDs = append(selection.SkillIDs, skill.ID)
	}
	for _, plugin := range options.Plugins {
		selection.PluginIDs = append(selection.PluginIDs, plugin.ID)
	}
	return selection, selection.Normalize()
}
