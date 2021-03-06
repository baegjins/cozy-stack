package jobs

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/cozy/cozy-stack/pkg/consts"
	"github.com/cozy/cozy-stack/pkg/couchdb"
	"github.com/cozy/cozy-stack/pkg/globals"
	"github.com/cozy/cozy-stack/pkg/jobs"
	"github.com/cozy/cozy-stack/pkg/scheduler"
	"github.com/cozy/cozy-stack/web/jsonapi"
	"github.com/cozy/cozy-stack/web/middlewares"
	"github.com/cozy/cozy-stack/web/permissions"
	multierror "github.com/hashicorp/go-multierror"
	"github.com/labstack/echo"

	// import workers
	_ "github.com/cozy/cozy-stack/pkg/workers/exec"
	_ "github.com/cozy/cozy-stack/pkg/workers/log"
	_ "github.com/cozy/cozy-stack/pkg/workers/mails"
	_ "github.com/cozy/cozy-stack/pkg/workers/sharings"
	_ "github.com/cozy/cozy-stack/pkg/workers/thumbnail"
	_ "github.com/cozy/cozy-stack/pkg/workers/unzip"
)

type (
	apiJob struct {
		j *jobs.Job
	}
	apiJobRequest struct {
		Arguments json.RawMessage  `json:"arguments"`
		Options   *jobs.JobOptions `json:"options"`
	}
	apiQueue struct {
		workerType string
	}
	apiTrigger struct {
		t scheduler.Trigger
	}
	apiTriggerRequest struct {
		Type            string           `json:"type"`
		Arguments       string           `json:"arguments"`
		WorkerType      string           `json:"worker"`
		WorkerArguments json.RawMessage  `json:"worker_arguments"`
		Debounce        string           `json:"debounce"`
		Options         *jobs.JobOptions `json:"options"`
	}
)

func (j *apiJob) ID() string                             { return j.j.ID() }
func (j *apiJob) Rev() string                            { return j.j.Rev() }
func (j *apiJob) DocType() string                        { return consts.Jobs }
func (j *apiJob) Clone() couchdb.Doc                     { return j }
func (j *apiJob) SetID(_ string)                         {}
func (j *apiJob) SetRev(_ string)                        {}
func (j *apiJob) Relationships() jsonapi.RelationshipMap { return nil }
func (j *apiJob) Included() []jsonapi.Object             { return nil }
func (j *apiJob) Links() *jsonapi.LinksList {
	return &jsonapi.LinksList{Self: "/jobs/" + j.j.WorkerType + "/" + j.j.ID()}
}
func (j *apiJob) MarshalJSON() ([]byte, error) {
	return json.Marshal(j.j)
}

func (q *apiQueue) ID() string      { return q.workerType }
func (q *apiQueue) DocType() string { return consts.Jobs }
func (q *apiQueue) Valid(key, value string) bool {
	switch key {
	case "worker":
		return q.workerType == value
	}
	return false
}

func (t *apiTrigger) ID() string                             { return t.t.Infos().TID }
func (t *apiTrigger) Rev() string                            { return "" }
func (t *apiTrigger) DocType() string                        { return consts.Triggers }
func (t *apiTrigger) Clone() couchdb.Doc                     { return t }
func (t *apiTrigger) SetID(_ string)                         {}
func (t *apiTrigger) SetRev(_ string)                        {}
func (t *apiTrigger) Relationships() jsonapi.RelationshipMap { return nil }
func (t *apiTrigger) Included() []jsonapi.Object             { return nil }
func (t *apiTrigger) Links() *jsonapi.LinksList {
	return &jsonapi.LinksList{Self: "/jobs/triggers/" + t.ID()}
}
func (t *apiTrigger) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.t.Infos())
}

func getQueue(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	workerType := c.Param("worker-type")

	o := &apiQueue{workerType: workerType}
	// TODO: uncomment to restric jobs permissions.
	// if err := permissions.AllowOnFields(c, permissions.GET, o, "worker"); err != nil {
	// 	return err
	// }
	if err := permissions.Allow(c, permissions.GET, o); err != nil {
		return err
	}

	js, err := jobs.GetQueuedJobs(instance.Domain, workerType)
	if err != nil {
		return wrapJobsError(err)
	}

	objs := make([]jsonapi.Object, len(js))
	for i, j := range js {
		objs[i] = &apiJob{j}
	}

	return jsonapi.DataList(c, http.StatusOK, objs, nil)
}

func pushJob(c echo.Context) error {
	instance := middlewares.GetInstance(c)

	req := &apiJobRequest{}
	if _, err := jsonapi.Bind(c.Request(), &req); err != nil {
		return wrapJobsError(err)
	}

	jr := &jobs.JobRequest{
		Domain:     instance.Domain,
		WorkerType: c.Param("worker-type"),
		Options:    req.Options,
		Message:    jobs.Message(req.Arguments),
	}
	// TODO: uncomment to restric jobs permissions.
	// if err := permissions.AllowOnFields(c, permissions.POST, jr, "worker"); err != nil {
	// 	return err
	// }
	if err := permissions.Allow(c, permissions.POST, jr); err != nil {
		return err
	}

	job, err := globals.GetBroker().PushJob(jr)
	if err != nil {
		return wrapJobsError(err)
	}

	return jsonapi.Data(c, http.StatusAccepted, &apiJob{job}, nil)
}

func newTrigger(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	sched := globals.GetScheduler()
	req := &apiTriggerRequest{}
	if _, err := jsonapi.Bind(c.Request(), &req); err != nil {
		return wrapJobsError(err)
	}

	if req.Debounce != "" {
		if _, err := time.ParseDuration(req.Debounce); err != nil {
			return jsonapi.InvalidAttribute("debounce", err)
		}
	}

	t, err := scheduler.NewTrigger(&scheduler.TriggerInfos{
		Type:       req.Type,
		WorkerType: req.WorkerType,
		Domain:     instance.Domain,
		Arguments:  req.Arguments,
		Debounce:   req.Debounce,
		Options:    req.Options,
		Message:    jobs.Message(req.WorkerArguments),
	})
	if err != nil {
		return wrapJobsError(err)
	}
	// TODO: uncomment to restric jobs permissions.
	// if err = permissions.AllowOnFields(c, permissions.POST, t, "worker"); err != nil {
	// 	return err
	// }
	if err = permissions.Allow(c, permissions.POST, t); err != nil {
		return err
	}

	if err = sched.Add(t); err != nil {
		return wrapJobsError(err)
	}
	return jsonapi.Data(c, http.StatusCreated, &apiTrigger{t}, nil)
}

func getTrigger(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	sched := globals.GetScheduler()
	t, err := sched.Get(instance.Domain, c.Param("trigger-id"))
	if err != nil {
		return wrapJobsError(err)
	}
	if err := permissions.Allow(c, permissions.GET, t); err != nil {
		return err
	}
	return jsonapi.Data(c, http.StatusOK, &apiTrigger{t}, nil)
}

func launchTrigger(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	t, err := globals.GetScheduler().Get(instance.Domain, c.Param("trigger-id"))
	if err != nil {
		return wrapJobsError(err)
	}
	if err = permissions.Allow(c, permissions.POST, t); err != nil {
		return err
	}
	j, err := globals.GetBroker().PushJob(t.Infos().JobRequest())
	if err != nil {
		return wrapJobsError(err)
	}
	return jsonapi.Data(c, http.StatusCreated, &apiJob{j}, nil)
}

func deleteTrigger(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	sched := globals.GetScheduler()
	t, err := sched.Get(instance.Domain, c.Param("trigger-id"))
	if err != nil {
		return wrapJobsError(err)
	}
	if err := permissions.Allow(c, permissions.DELETE, t); err != nil {
		return err
	}
	if err := sched.Delete(instance.Domain, c.Param("trigger-id")); err != nil {
		return wrapJobsError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func getAllTriggers(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	workerFilter := c.QueryParam("Worker")
	sched := globals.GetScheduler()
	if err := permissions.AllowWholeType(c, permissions.GET, consts.Triggers); err != nil {
		return err
	}
	ts, err := sched.GetAll(instance.Domain)
	if err != nil {
		return wrapJobsError(err)
	}
	// TODO: we could potentially benefit from an index on 'worker_type' field.
	objs := make([]jsonapi.Object, 0, len(ts))
	for _, t := range ts {
		if workerFilter == "" || t.Infos().WorkerType == workerFilter {
			objs = append(objs, &apiTrigger{t})
		}
	}
	return jsonapi.DataList(c, http.StatusOK, objs, nil)
}

func getJob(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	job, err := jobs.Get(instance.Domain, c.Param("job-id"))
	if err != nil {
		return err
	}
	if err := permissions.Allow(c, permissions.GET, job); err != nil {
		return err
	}
	return jsonapi.Data(c, http.StatusOK, &apiJob{job}, nil)
}

func cleanJobs(c echo.Context) error {
	instance := middlewares.GetInstance(c)
	if err := permissions.AllowWholeType(c, permissions.GET, consts.Jobs); err != nil {
		return err
	}
	var ups []*jobs.Job
	now := time.Now()
	err := couchdb.ForeachDocs(instance, consts.Jobs, func(data []byte) error {
		var job *jobs.Job
		if err := json.Unmarshal(data, &job); err != nil {
			return err
		}
		if job.State != jobs.Running {
			return nil
		}
		if job.StartedAt.Add(1 * time.Hour).Before(now) {
			ups = append(ups, job)
		}
		return nil
	})
	if err != nil && !couchdb.IsNoDatabaseError(err) {
		return err
	}
	var errf error
	for _, j := range ups {
		j.State = jobs.Done
		err := couchdb.UpdateDoc(instance, j)
		if err != nil {
			errf = multierror.Append(errf, err)
		}
	}
	if errf != nil {
		return errf
	}
	return c.JSON(200, map[string]int{"deleted": len(ups)})
}

// Routes sets the routing for the jobs service
func Routes(router *echo.Group) {
	router.GET("/queue/:worker-type", getQueue)
	router.POST("/queue/:worker-type", pushJob)

	router.GET("/triggers", getAllTriggers)
	router.POST("/triggers", newTrigger)
	router.GET("/triggers/:trigger-id", getTrigger)
	router.POST("/triggers/:trigger-id/launch", launchTrigger)
	router.DELETE("/triggers/:trigger-id", deleteTrigger)

	router.POST("/clean", cleanJobs)
	router.GET("/:job-id", getJob)
}

func wrapJobsError(err error) error {
	switch err {
	case scheduler.ErrNotFoundTrigger,
		jobs.ErrNotFoundJob,
		jobs.ErrUnknownWorker:
		return jsonapi.NotFound(err)
	case scheduler.ErrUnknownTrigger:
		return jsonapi.InvalidAttribute("Type", err)
	}
	return err
}
