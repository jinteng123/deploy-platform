package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"deploy-platform/backend-go/internal/service"
	"github.com/gin-gonic/gin"
)

func registerDeliveryIncidentAPI(api *gin.RouterGroup, store *service.Store) {
	api.GET("/delivery/incidents", requireRole(store, service.RoleViewer), func(c *gin.Context) {
		limit := service.DefaultDeliveryIncidentLimit
		if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > service.MaxDeliveryIncidentLimit {
				fail(c, http.StatusBadRequest, "limit 必须在 1 到 100 之间")
				return
			}
			limit = parsed
		}
		rows, err := store.DeliveryIncidents(limit)
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, rows)
	})

	api.POST("/delivery/incidents", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		var body struct {
			DeployLogID    int64     `json:"deploy_log_id" binding:"required,min=1"`
			OccurredAt     time.Time `json:"occurred_at" binding:"required"`
			Summary        string    `json:"summary" binding:"required,min=1,max=1000"`
			IdempotencyKey string    `json:"idempotency_key" binding:"required,min=8,max=128"`
		}
		if !bindJSON(c, &body) {
			return
		}
		user := currentUser(c)
		username := user.Username
		incident, err := store.CreateDeliveryIncident(body.DeployLogID, body.OccurredAt, body.Summary, username, body.IdempotencyKey)
		audit(c, store, "delivery_incident_record", incident.ClusterID, "", "delivery_incidents", strconv.FormatInt(incident.ID, 10), "linked_deploy_log_id="+strconv.FormatInt(body.DeployLogID, 10), auditErr(err))
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusCreated, incident)
	})

	api.POST("/delivery/incidents/:id/recovery", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var body struct {
			ExpectedVersion *int64    `json:"expected_version" binding:"required,min=1"`
			OccurredAt      time.Time `json:"occurred_at" binding:"required"`
			Confirmation    string    `json:"confirmation" binding:"required,min=1,max=1000"`
			IdempotencyKey  string    `json:"idempotency_key" binding:"required,min=8,max=128"`
		}
		if !bindJSON(c, &body) {
			return
		}
		incident, changed, err := store.ConfirmDeliveryIncidentRecovery(id, *body.ExpectedVersion, body.OccurredAt, body.Confirmation, currentUser(c).Username, body.IdempotencyKey)
		audit(c, store, "delivery_incident_recovery_confirm", incident.ClusterID, "", "delivery_incidents", strconv.FormatInt(id, 10), "changed="+strconv.FormatBool(changed), auditErr(err))
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, incident)
	})

	api.POST("/delivery/incidents/:id/causality", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var body struct {
			ExpectedVersion *int64 `json:"expected_version" binding:"required,min=1"`
			Status          string `json:"status" binding:"required"`
			Reason          string `json:"reason" binding:"required,min=1,max=1000"`
			IdempotencyKey  string `json:"idempotency_key" binding:"required,min=8,max=128"`
		}
		if !bindJSON(c, &body) {
			return
		}
		incident, changed, err := store.AssessDeliveryIncidentCausality(id, *body.ExpectedVersion, body.Status, body.Reason, currentUser(c).Username, body.IdempotencyKey)
		audit(c, store, "delivery_incident_causality_assess", incident.ClusterID, "", "delivery_incidents", strconv.FormatInt(id, 10), "status="+body.Status+";changed="+strconv.FormatBool(changed), auditErr(err))
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, incident)
	})

	api.POST("/delivery/incidents/:id/remediation", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var body struct {
			ExpectedVersion        *int64 `json:"expected_version" binding:"required,min=1"`
			RemediationType        string `json:"remediation_type" binding:"required"`
			PlanStatus             string `json:"plan_status" binding:"required"`
			RemediationDeployLogID int64  `json:"remediation_deploy_log_id"`
			Reason                 string `json:"reason" binding:"required,min=1,max=1000"`
			IdempotencyKey         string `json:"idempotency_key" binding:"required,min=8,max=128"`
		}
		if !bindJSON(c, &body) {
			return
		}
		incident, changed, err := store.RecordDeliveryIncidentRemediation(
			id, *body.ExpectedVersion, body.RemediationType, body.PlanStatus,
			body.RemediationDeployLogID, body.Reason, currentUser(c).Username, body.IdempotencyKey,
		)
		audit(c, store, "delivery_incident_remediation_record", incident.ClusterID, "", "delivery_incidents", strconv.FormatInt(id, 10), "type="+body.RemediationType+";plan_status="+body.PlanStatus+";changed="+strconv.FormatBool(changed), auditErr(err))
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, incident)
	})

	api.POST("/delivery/incidents/:id/intervention", requireRole(store, service.RoleOperator), func(c *gin.Context) {
		id, ok := pathInt64(c, "id")
		if !ok {
			return
		}
		var body struct {
			ExpectedVersion *int64    `json:"expected_version" binding:"required,min=1"`
			OccurredAt      time.Time `json:"occurred_at" binding:"required"`
			Detail          string    `json:"detail" binding:"required,min=1,max=1000"`
			IdempotencyKey  string    `json:"idempotency_key" binding:"required,min=8,max=128"`
		}
		if !bindJSON(c, &body) {
			return
		}
		incident, changed, err := store.StartDeliveryIncidentIntervention(id, *body.ExpectedVersion, body.OccurredAt, body.Detail, currentUser(c).Username, body.IdempotencyKey)
		audit(c, store, "delivery_incident_intervention_start", incident.ClusterID, "", "delivery_incidents", strconv.FormatInt(id, 10), "changed="+strconv.FormatBool(changed), auditErr(err))
		if err != nil {
			writeStoreErr(c, err)
			return
		}
		c.JSON(http.StatusOK, incident)
	})
}
