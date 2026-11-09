package fake

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	p "github.com/Cosss7/tina/packages/protocol"
	"github.com/Cosss7/tina/services/tinad/internal/engine"
)

// Scripted is a deterministic offline engine. Its model outputs are fixtures;
// production runtime code never interprets plans or reports.
type Scripted struct{ *Driver }

func NewScripted() *Scripted { return &Scripted{New()} }
func (d *Scripted) Create(ctx context.Context, o engine.OpenOptions) (engine.Session, error) {
	s, err := d.Driver.Create(ctx, o)
	if err != nil {
		return nil, err
	}
	return &scriptedSession{Session: s.(*Session), options: o.Config.Options}, nil
}
func (d *Scripted) Resume(ctx context.Context, h p.SessionHandle, o engine.OpenOptions) (engine.Session, error) {
	s, err := d.Driver.Resume(ctx, h, o)
	if err != nil {
		return nil, err
	}
	return &scriptedSession{Session: s.(*Session), options: o.Config.Options}, nil
}

type scriptedSession struct {
	*Session
	options map[string]json.RawMessage
}

func (s *scriptedSession) Submit(ctx context.Context, in p.Input) (p.Receipt, error) {
	r, err := s.Session.Submit(ctx, in)
	if err != nil || r.Admission != p.Accepted {
		return r, err
	}
	delay := 100
	json.Unmarshal(s.options["delayMs"], &delay)
	if delay < 0 {
		return r, nil
	} // A controlled running activity for cancellation tests.
	if delay > 60000 {
		delay = 60000
	}
	go func() {
		time.Sleep(time.Duration(delay) * time.Millisecond)
		output := `{"state":"done","summary":"按验收标准完成了离线演示","decisions":[],"artifacts":[],"blockers":[],"usage":{}}`
		if strings.Contains(in.Text, "[TINA:PLAN]") {
			output = `{"summary":"先完成首页，再接通接口","cards":[{"key":"c1","title":"首页改版","description":"调整首屏信息层级","acceptance":["主要入口可见"],"deps":[]},{"key":"c2","title":"接口联调","description":"接通首页接口","acceptance":["错误情况可诊断"],"deps":["c1"]}]}`
		} else if strings.Contains(in.Text, "标题：接口联调") {
			output = `{"state":"blocked","summary":"部分接口仍需新的凭据","decisions":[],"artifacts":[],"blockers":["订单接口缺少 API key"],"usage":{}}`
		}
		s.Complete(in.Execution.ActivityID, output)
	}()
	return r, nil
}
