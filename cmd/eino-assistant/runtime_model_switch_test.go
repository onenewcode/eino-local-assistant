package main

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"eino-local-assistant/internal/agent"
	"eino-local-assistant/internal/chat"
	"eino-local-assistant/internal/config"
	"eino-local-assistant/internal/contextbuild"
	"eino-local-assistant/internal/store"
	"eino-local-assistant/internal/tools"
	"eino-local-assistant/internal/usage"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type runtimeFactoryChatModel struct {
	name string
}

func (m *runtimeFactoryChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("runtime factory response", nil), nil
}

func (m *runtimeFactoryChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{}), nil
}

func (m *runtimeFactoryChatModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

type runtimeFactoryCompactor struct{}

func (runtimeFactoryCompactor) Compact(context.Context, contextbuild.CompactionRequest, contextbuild.CompactionUsageObserver) (contextbuild.Checkpoint, error) {
	return contextbuild.Checkpoint{}, nil
}

type runtimeSessionModel struct{}

func (runtimeSessionModel) Stream(context.Context, []*schema.Message) (chat.Stream, error) {
	return runtimeSessionStream{}, nil
}

type runtimeSessionStream struct{}

func (runtimeSessionStream) Recv() (*schema.Message, error) { return nil, io.EOF }
func (runtimeSessionStream) Close()                         {}

type runtimeMetaLoaderFunc func(context.Context, string) (store.ThreadMeta, error)

func (f runtimeMetaLoaderFunc) LoadThreadMeta(ctx context.Context, id string) (store.ThreadMeta, error) {
	return f(ctx, id)
}

func validRuntimeConfig(modelName string) config.Config {
	return config.Config{
		Model: config.ModelConfig{
			Provider:       config.ProviderOpenAI,
			BaseURL:        "https://api.example.test/v1",
			APIKey:         "test-key",
			Name:           modelName,
			TimeoutSeconds: 30,
			Context: config.ModelContextConfig{
				WindowTokens:              16000,
				MaxOutputTokens:           2000,
				AutoCompactTriggerPercent: 75,
				PostCompactTargetPercent:  45,
				LowGainThresholdPercent:   10,
			},
			Pricing: config.PricingConfig{InputPerMillion: 1, OutputPerMillion: 2},
		},
	}
}

func newRuntimeModelSwitchFixture(t *testing.T) (*commandRuntime, *chat.Session, *store.ThreadStore) {
	t.Helper()
	threadStore, err := store.NewThreadStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewThreadStore: %v", err)
	}
	oldSessionModel := runtimeSessionModel{}
	session, err := chat.NewSession(oldSessionModel, "frozen runtime prompt", chat.SessionOptions{
		Store:     threadStore,
		ModelName: "old-model",
		Pricing:   usage.Pricing{InputPerMillion: 1, OutputPerMillion: 2},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	oldProvider := &runtimeFactoryChatModel{name: "old-model"}
	oldReact := &agent.ReActModel{}
	r := &commandRuntime{
		cfg:            validRuntimeConfig("old-model"),
		session:        session,
		sessionStore:   threadStore,
		chatModel:      oldProvider,
		registry:       tools.New(),
		reactModel:     oldReact,
		taskController: agent.NewTaskController(),
		runtimeCfg:     config.RuntimeConfig{MaxReactSteps: 7},
		sessionOpts: chat.SessionOptions{
			Store:     threadStore,
			ModelName: "old-model",
			Pricing:   usage.Pricing{InputPerMillion: 1, OutputPerMillion: 2},
		},
		modelFactory: defaultRuntimeModelFactory(),
	}
	r.modelFactory = runtimeModelFactory{
		newChatModel: func(context.Context, config.ModelConfig) (model.ToolCallingChatModel, error) {
			return &runtimeFactoryChatModel{name: "candidate"}, nil
		},
		newReActModel: func(_ context.Context, _ model.ToolCallingChatModel, _ []tool.BaseTool, opts agent.ReActOptions) (*agent.ReActModel, error) {
			if opts.TaskController != r.taskController {
				t.Fatalf("candidate did not reuse task controller")
			}
			return &agent.ReActModel{}, nil
		},
		newCompactor: func(model.BaseChatModel, contextbuild.Config) (contextbuild.CheckpointCompactor, error) {
			return runtimeFactoryCompactor{}, nil
		},
	}
	return r, session, threadStore
}

func TestRuntimeBuildModelBundleOrderAndSharedTaskController(t *testing.T) {
	r, _, _ := newRuntimeModelSwitchFixture(t)
	var order []string
	var raw model.ToolCallingChatModel
	r.modelFactory = runtimeModelFactory{
		newChatModel: func(_ context.Context, cfg config.ModelConfig) (model.ToolCallingChatModel, error) {
			order = append(order, "provider:"+cfg.Name)
			raw = &runtimeFactoryChatModel{name: cfg.Name}
			return raw, nil
		},
		newReActModel: func(_ context.Context, got model.ToolCallingChatModel, _ []tool.BaseTool, opts agent.ReActOptions) (*agent.ReActModel, error) {
			order = append(order, "react")
			if got != raw || opts.TaskController != r.taskController {
				t.Fatal("ReAct factory did not receive the candidate provider and shared task controller")
			}
			return &agent.ReActModel{}, nil
		},
		newCompactor: func(got model.BaseChatModel, _ contextbuild.Config) (contextbuild.CheckpointCompactor, error) {
			order = append(order, "compactor")
			if got != raw {
				t.Fatal("compactor did not receive the raw candidate provider")
			}
			return runtimeFactoryCompactor{}, nil
		},
	}
	cfg := validRuntimeConfig("new-model")
	bundle, err := r.buildModelBundle(context.Background(), cfg, false)
	if err != nil {
		t.Fatalf("buildModelBundle: %v", err)
	}
	if !reflect.DeepEqual(order, []string{"provider:new-model", "react", "compactor"}) {
		t.Fatalf("construction order=%v", order)
	}
	if bundle.sessionOpts.ModelName != "new-model" || bundle.sessionOpts.Store != r.sessionStore || bundle.compactor == nil {
		t.Fatalf("candidate session options=%#v", bundle.sessionOpts)
	}
}

func TestRuntimeSwitchModelCommitsCandidateInPlace(t *testing.T) {
	r, session, threadStore := newRuntimeModelSwitchFixture(t)
	oldSessionModel := session.Model()
	oldID := session.ID()
	bundle, err := r.switchModel(context.Background(), session, "new-model")
	if err != nil {
		t.Fatalf("switchModel: %v", err)
	}
	if r.session != session || session.ID() != oldID || session.Model() != bundle.reactModel || session.Model() == oldSessionModel {
		t.Fatalf("switch did not commit in place: runtime session=%p active=%p model=%p candidate=%p old=%p", r.session, session, session.Model(), bundle.reactModel, oldSessionModel)
	}
	if session.ModelName() != "new-model" || r.cfg.Model.Name != "new-model" || r.sessionOpts.ModelName != "new-model" {
		t.Fatalf("model identity after switch: session=%q cfg=%q opts=%q", session.ModelName(), r.cfg.Model.Name, r.sessionOpts.ModelName)
	}
	state, err := threadStore.LoadThread(context.Background(), oldID)
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if state.Meta.Model != "new-model" {
		t.Fatalf("durable model=%q, want new-model", state.Meta.Model)
	}
}

func TestRuntimeCandidateFailureDoesNotPolluteOldBundle(t *testing.T) {
	cases := []struct {
		name        string
		makeFactory func(*commandRuntime) runtimeModelFactory
	}{
		{
			name: "provider",
			makeFactory: func(r *commandRuntime) runtimeModelFactory {
				return runtimeModelFactory{
					newChatModel: func(context.Context, config.ModelConfig) (model.ToolCallingChatModel, error) {
						return nil, errors.New("provider failed")
					},
				}
			},
		},
		{
			name: "react",
			makeFactory: func(r *commandRuntime) runtimeModelFactory {
				return runtimeModelFactory{
					newChatModel: func(context.Context, config.ModelConfig) (model.ToolCallingChatModel, error) {
						return &runtimeFactoryChatModel{name: "candidate"}, nil
					},
					newReActModel: func(context.Context, model.ToolCallingChatModel, []tool.BaseTool, agent.ReActOptions) (*agent.ReActModel, error) {
						return nil, errors.New("react failed")
					},
				}
			},
		},
		{
			name: "compactor",
			makeFactory: func(r *commandRuntime) runtimeModelFactory {
				return runtimeModelFactory{
					newChatModel: func(context.Context, config.ModelConfig) (model.ToolCallingChatModel, error) {
						return &runtimeFactoryChatModel{name: "candidate"}, nil
					},
					newReActModel: func(context.Context, model.ToolCallingChatModel, []tool.BaseTool, agent.ReActOptions) (*agent.ReActModel, error) {
						return &agent.ReActModel{}, nil
					},
					newCompactor: func(model.BaseChatModel, contextbuild.Config) (contextbuild.CheckpointCompactor, error) {
						return nil, errors.New("compactor failed")
					},
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, session, threadStore := newRuntimeModelSwitchFixture(t)
			oldCfg, oldProvider, oldReact, oldOpts := r.modelSnapshot()
			oldSessionModel := session.Model()
			oldSessionName := session.ModelName()
			before, err := threadStore.LoadThread(context.Background(), session.ID())
			if err != nil {
				t.Fatalf("LoadThread before switch: %v", err)
			}
			r.modelFactory = tc.makeFactory(r)
			if _, err := r.switchModel(context.Background(), session, "new-model"); err == nil {
				t.Fatal("switchModel unexpectedly succeeded")
			}
			gotCfg, gotProvider, gotReact, gotOpts := r.modelSnapshot()
			if !reflect.DeepEqual(gotCfg, oldCfg) || gotProvider != oldProvider || gotReact != oldReact || !reflect.DeepEqual(gotOpts, oldOpts) {
				t.Fatalf("failed %s build polluted runtime: cfg=%#v provider=%p react=%p opts=%#v", tc.name, gotCfg, gotProvider, gotReact, gotOpts)
			}
			if session.Model() != oldSessionModel || session.ModelName() != oldSessionName {
				t.Fatalf("failed %s build polluted session: model=%p name=%q", tc.name, session.Model(), session.ModelName())
			}
			after, err := threadStore.LoadThread(context.Background(), session.ID())
			if err != nil {
				t.Fatalf("LoadThread after switch: %v", err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("failed %s build changed durable state: before=%#v after=%#v", tc.name, before, after)
			}
		})
	}
}

func TestRuntimeOpenSessionUsesTargetDurableModelBeforeBuilding(t *testing.T) {
	r, active, threadStore := newRuntimeModelSwitchFixture(t)
	target, err := chat.NewSession(runtimeSessionModel{}, "target prompt", chat.SessionOptions{
		Store:     threadStore,
		ModelName: "durable-target",
	})
	if err != nil {
		t.Fatalf("target NewSession: %v", err)
	}
	var providerName string
	r.modelFactory = runtimeModelFactory{
		newChatModel: func(_ context.Context, cfg config.ModelConfig) (model.ToolCallingChatModel, error) {
			providerName = cfg.Name
			return &runtimeFactoryChatModel{name: cfg.Name}, nil
		},
		newReActModel: func(context.Context, model.ToolCallingChatModel, []tool.BaseTool, agent.ReActOptions) (*agent.ReActModel, error) {
			return &agent.ReActModel{}, nil
		},
		newCompactor: func(model.BaseChatModel, contextbuild.Config) (contextbuild.CheckpointCompactor, error) {
			return runtimeFactoryCompactor{}, nil
		},
	}
	opened, err := r.openSession(context.Background(), target.ID(), false)
	if err != nil {
		t.Fatalf("openSession: %v", err)
	}
	if providerName != "durable-target" || opened.session != r.session || opened.session.ID() != target.ID() || r.session == active {
		t.Fatalf("open session model/session selection: provider=%q opened=%p runtime=%p active=%p", providerName, opened.session, r.session, active)
	}
	if r.cfg.Model.Name != "durable-target" || r.sessionOpts.ModelName != "durable-target" {
		t.Fatalf("runtime model identity after resume: cfg=%q opts=%q", r.cfg.Model.Name, r.sessionOpts.ModelName)
	}
}

func TestRuntimeOpenSessionFailureLeavesOldBundleUnchanged(t *testing.T) {
	r, active, threadStore := newRuntimeModelSwitchFixture(t)
	target, err := chat.NewSession(runtimeSessionModel{}, "target prompt", chat.SessionOptions{Store: threadStore, ModelName: "durable-target"})
	if err != nil {
		t.Fatalf("target NewSession: %v", err)
	}
	state, err := threadStore.LoadThread(context.Background(), target.ID())
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if _, err = threadStore.StartTurn(context.Background(), target.ID(), state.Revision, store.TurnStart{TurnID: "active-target", Input: "unfinished"}); err != nil {
		t.Fatalf("StartTurn: %v", err)
	}
	oldCfg, oldProvider, oldReact, oldOpts := r.modelSnapshot()
	r.modelFactory = runtimeModelFactory{
		newChatModel: func(_ context.Context, cfg config.ModelConfig) (model.ToolCallingChatModel, error) {
			if cfg.Name != "durable-target" {
				t.Fatalf("open failure built wrong target model %q", cfg.Name)
			}
			return &runtimeFactoryChatModel{name: cfg.Name}, nil
		},
		newReActModel: func(context.Context, model.ToolCallingChatModel, []tool.BaseTool, agent.ReActOptions) (*agent.ReActModel, error) {
			return &agent.ReActModel{}, nil
		},
		newCompactor: func(model.BaseChatModel, contextbuild.Config) (contextbuild.CheckpointCompactor, error) {
			return runtimeFactoryCompactor{}, nil
		},
	}
	if _, err := r.openSession(context.Background(), target.ID(), false); err == nil {
		t.Fatal("openSession unexpectedly recovered an active target without authorization")
	}
	gotCfg, gotProvider, gotReact, gotOpts := r.modelSnapshot()
	if !reflect.DeepEqual(gotCfg, oldCfg) || gotProvider != oldProvider || gotReact != oldReact || !reflect.DeepEqual(gotOpts, oldOpts) || r.session != active {
		t.Fatalf("failed resume polluted runtime: cfg=%#v provider=%p react=%p opts=%#v session=%p", gotCfg, gotProvider, gotReact, gotOpts, r.session)
	}
}

func TestResolveStartupModelConfigUsesDurableTargetBeforeBundle(t *testing.T) {
	r, _, threadStore := newRuntimeModelSwitchFixture(t)
	target, err := chat.NewSession(runtimeSessionModel{}, "target prompt", chat.SessionOptions{
		Store:     threadStore,
		ModelName: "durable-target",
	})
	if err != nil {
		t.Fatalf("target NewSession: %v", err)
	}

	cfg, err := resolveStartupModelConfig(context.Background(), validRuntimeConfig("configured-model"), sessionStart{
		resumeID: target.ID(),
	}, threadStore)
	if err != nil {
		t.Fatalf("resolveStartupModelConfig: %v", err)
	}
	var providerName string
	r.modelFactory = runtimeModelFactory{
		newChatModel: func(_ context.Context, got config.ModelConfig) (model.ToolCallingChatModel, error) {
			providerName = got.Name
			return &runtimeFactoryChatModel{name: got.Name}, nil
		},
		newReActModel: func(context.Context, model.ToolCallingChatModel, []tool.BaseTool, agent.ReActOptions) (*agent.ReActModel, error) {
			return &agent.ReActModel{}, nil
		},
		newCompactor: func(model.BaseChatModel, contextbuild.Config) (contextbuild.CheckpointCompactor, error) {
			return runtimeFactoryCompactor{}, nil
		},
	}
	bundle, err := r.buildModelBundle(context.Background(), cfg, false)
	if err != nil {
		t.Fatalf("buildModelBundle: %v", err)
	}
	if providerName != "durable-target" || bundle.sessionOpts.ModelName != "durable-target" {
		t.Fatalf("startup model identity: provider=%q opts=%q", providerName, bundle.sessionOpts.ModelName)
	}
}

func TestResolveStartupModelConfigExplicitOverrideWinsWithoutMetadataLoad(t *testing.T) {
	r, _, _ := newRuntimeModelSwitchFixture(t)
	called := false
	loader := runtimeMetaLoaderFunc(func(context.Context, string) (store.ThreadMeta, error) {
		called = true
		return store.ThreadMeta{}, errors.New("metadata must not be loaded")
	})
	cfg, err := resolveStartupModelConfig(context.Background(), validRuntimeConfig("configured-model"), sessionStart{
		resumeID:  "durable-target",
		modelName: "explicit-model",
	}, loader)
	if err != nil {
		t.Fatalf("resolveStartupModelConfig: %v", err)
	}
	if called || cfg.Model.Name != "explicit-model" {
		t.Fatalf("explicit startup model: loaded=%v name=%q", called, cfg.Model.Name)
	}

	var providerName string
	r.modelFactory = runtimeModelFactory{
		newChatModel: func(_ context.Context, got config.ModelConfig) (model.ToolCallingChatModel, error) {
			providerName = got.Name
			return &runtimeFactoryChatModel{name: got.Name}, nil
		},
		newReActModel: func(context.Context, model.ToolCallingChatModel, []tool.BaseTool, agent.ReActOptions) (*agent.ReActModel, error) {
			return &agent.ReActModel{}, nil
		},
		newCompactor: func(model.BaseChatModel, contextbuild.Config) (contextbuild.CheckpointCompactor, error) {
			return runtimeFactoryCompactor{}, nil
		},
	}
	bundle, err := r.buildModelBundle(context.Background(), cfg, false)
	if err != nil {
		t.Fatalf("buildModelBundle: %v", err)
	}
	if providerName != "explicit-model" || bundle.sessionOpts.ModelName != "explicit-model" {
		t.Fatalf("explicit model identity: provider=%q opts=%q", providerName, bundle.sessionOpts.ModelName)
	}
}

func TestResolveStartupModelConfigPropagatesMetadataLoadFailure(t *testing.T) {
	loader := runtimeMetaLoaderFunc(func(context.Context, string) (store.ThreadMeta, error) {
		return store.ThreadMeta{}, errors.New("metadata unavailable")
	})
	_, err := resolveStartupModelConfig(context.Background(), validRuntimeConfig("configured-model"), sessionStart{
		resumeID: "durable-target",
	}, loader)
	if err == nil || !strings.Contains(err.Error(), "metadata unavailable") {
		t.Fatalf("metadata load error=%v, want propagated failure", err)
	}
}

func TestResolveStartupModelConfigFallsBackForLegacyEmptyModel(t *testing.T) {
	loader := runtimeMetaLoaderFunc(func(context.Context, string) (store.ThreadMeta, error) {
		return store.ThreadMeta{}, nil
	})
	cfg, err := resolveStartupModelConfig(context.Background(), validRuntimeConfig("configured-model"), sessionStart{
		resumeID: "legacy-thread",
	}, loader)
	if err != nil {
		t.Fatalf("resolveStartupModelConfig: %v", err)
	}
	if cfg.Model.Name != "configured-model" {
		t.Fatalf("legacy startup model=%q, want configured-model", cfg.Model.Name)
	}
}
