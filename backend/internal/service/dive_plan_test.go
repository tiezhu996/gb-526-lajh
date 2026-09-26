package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"commercial-diving-decompression-control/backend/internal/audit"
	"commercial-diving-decompression-control/backend/internal/constants"
	"commercial-diving-decompression-control/backend/internal/dto"
	"commercial-diving-decompression-control/backend/internal/model"
	"commercial-diving-decompression-control/backend/internal/repository"
	"commercial-diving-decompression-control/backend/internal/util"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newReuseTestService(t *testing.T) (*DivePlanService, *gorm.DB) {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.AutoMigrate(&model.DiverProfile{}, &model.DivePlan{}, &model.ExposureSegment{}, &audit.Event{}); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	auditRepo := audit.NewRepository(db)
	plans := repository.NewDivePlanRepository(db, auditRepo)
	profiles := repository.NewDiverProfileRepository(db, auditRepo)
	return NewDivePlanService(plans, profiles), db
}

func seedReuseTemplate(t *testing.T, db *gorm.DB, status constants.PlanStatus) (model.DiverProfile, model.DivePlan) {
	t.Helper()
	profile := model.DiverProfile{ProfileCode: "TRN-REUSE", DisplayName: "Reuse Target", QualificationLevel: "commercial", DefaultO2Fraction: 0.21, ProfileStatus: "active", Version: 1}
	if err := db.Create(&profile).Error; err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	mix := `{"o2":0.21,"he":0,"n2":0.79}`
	plan := model.DivePlan{PlanCode: "TEMPLATE-30A", DiverProfileID: profile.ID, WorksitePressureBar: 1, BreathingMixJSON: mix, PlanStatus: status, CreatedBy: 1, Version: 4, PlannedAt: time.Now().UTC()}
	if err := db.Create(&plan).Error; err != nil {
		t.Fatalf("seed plan: %v", err)
	}
	segments := []model.ExposureSegment{
		{PlanID: plan.ID, SequenceNo: 1, DepthM: 30, DurationMin: 3, GasMixJSON: mix, SegmentType: "descent", Notes: "down"},
		{PlanID: plan.ID, SequenceNo: 2, DepthM: 30, DurationMin: 24, GasMixJSON: mix, SegmentType: "bottom", Notes: "work"},
		{PlanID: plan.ID, SequenceNo: 3, DepthM: 0, DurationMin: 2, AscentRateMMin: 8, GasMixJSON: mix, SegmentType: "ascent", Notes: "up"},
	}
	if err := db.Create(&segments).Error; err != nil {
		t.Fatalf("seed segments: %v", err)
	}
	return profile, plan
}

func reuseActor() audit.Entry {
	return audit.Entry{RequestID: "test-request", ActorID: 7, ActorUsername: "planner"}
}

func TestReuseRejectsNonApprovedTemplate(t *testing.T) {
	service, db := newReuseTestService(t)
	profile, plan := seedReuseTemplate(t, db, constants.PlanDraft)
	_, _, err := service.Reuse(context.Background(), plan.ID, dto.ReuseDivePlanRequest{DiverProfileID: profile.ID}, reuseActor())
	var appErr *util.AppError
	if !errors.As(err, &appErr) || appErr.Code != "PLAN_NOT_REUSABLE" {
		t.Fatalf("expected PLAN_NOT_REUSABLE, got %v", err)
	}
}

func TestReuseClonesSegmentsIntoFreshDraft(t *testing.T) {
	service, db := newReuseTestService(t)
	profile, plan := seedReuseTemplate(t, db, constants.PlanApprovedTraining)
	response, created, err := service.Reuse(context.Background(), plan.ID, dto.ReuseDivePlanRequest{DiverProfileID: profile.ID}, reuseActor())
	if err != nil {
		t.Fatalf("reuse approved template: %v", err)
	}
	if !created {
		t.Fatal("expected a new copy to be created")
	}
	if response.PlanStatus != constants.PlanDraft || response.Version != 1 {
		t.Fatalf("copy must start as draft v1, got %s v%d", response.PlanStatus, response.Version)
	}
	if response.DiverProfileID != profile.ID || response.DiverProfileCode != profile.ProfileCode {
		t.Fatalf("copy must belong to target profile, got %+v", response)
	}
	if response.SourcePlanID == nil || *response.SourcePlanID != plan.ID || response.SourcePlanCode != plan.PlanCode {
		t.Fatalf("copy must reference source plan, got %+v", response)
	}
	if response.PlanCode == plan.PlanCode {
		t.Fatal("copy must receive its own plan_code")
	}
	var segments []model.ExposureSegment
	if err := db.Where("plan_id = ?", response.ID).Order("sequence_no ASC").Find(&segments).Error; err != nil {
		t.Fatalf("load copied segments: %v", err)
	}
	if len(segments) != 3 {
		t.Fatalf("expected 3 copied segments, got %d", len(segments))
	}
	for index, segment := range segments {
		if segment.SequenceNo != index+1 {
			t.Fatalf("sequence numbers must restart at 1 and stay contiguous, got %d at index %d", segment.SequenceNo, index)
		}
	}
	if segments[0].SegmentType != "descent" || segments[1].DepthM != 30 || segments[1].DurationMin != 24 || segments[2].AscentRateMMin != 8 {
		t.Fatalf("segment depth/duration/ascent/type must carry over in order, got %+v", segments)
	}
	if segments[0].GasMixJSON != plan.BreathingMixJSON {
		t.Fatalf("segment gas mix must carry over, got %s", segments[0].GasMixJSON)
	}
	var assessmentCount int64
	if err := db.Table("decompression_assessments").Where("plan_id = ?", response.ID).Count(&assessmentCount).Error; err == nil && assessmentCount != 0 {
		t.Fatalf("assessment results must not be copied, got %d", assessmentCount)
	}
	var event audit.Event
	if err := db.Where("action = ? AND entity_id = ?", "dive_plan.reuse", response.ID).First(&event).Error; err != nil {
		t.Fatalf("reuse must be recorded in audit: %v", err)
	}
	if event.ActorUsername != "planner" || event.EntityType != "dive_plan" {
		t.Fatalf("audit event must identify actor and entity, got %+v", event)
	}
}

func TestReuseReturnsExistingUnarchivedCopy(t *testing.T) {
	service, db := newReuseTestService(t)
	profile, plan := seedReuseTemplate(t, db, constants.PlanApprovedTraining)
	first, created, err := service.Reuse(context.Background(), plan.ID, dto.ReuseDivePlanRequest{DiverProfileID: profile.ID}, reuseActor())
	if err != nil || !created {
		t.Fatalf("first reuse must create a copy: created=%v err=%v", created, err)
	}
	second, created, err := service.Reuse(context.Background(), plan.ID, dto.ReuseDivePlanRequest{DiverProfileID: profile.ID}, reuseActor())
	if err != nil {
		t.Fatalf("second reuse: %v", err)
	}
	if created || second.ID != first.ID {
		t.Fatalf("second reuse must return the existing copy %d, got id=%d created=%v", first.ID, second.ID, created)
	}
	var planCount int64
	if err := db.Model(&model.DivePlan{}).Where("source_plan_id = ?", plan.ID).Count(&planCount).Error; err != nil {
		t.Fatalf("count copies: %v", err)
	}
	if planCount != 1 {
		t.Fatalf("template must keep a single unarchived copy per profile, got %d", planCount)
	}
}

func TestReuseCreatesNewCopyAfterPreviousArchived(t *testing.T) {
	service, db := newReuseTestService(t)
	profile, plan := seedReuseTemplate(t, db, constants.PlanApprovedTraining)
	first, _, err := service.Reuse(context.Background(), plan.ID, dto.ReuseDivePlanRequest{DiverProfileID: profile.ID}, reuseActor())
	if err != nil {
		t.Fatalf("first reuse: %v", err)
	}
	if err := db.Model(&model.DivePlan{}).Where("id = ?", first.ID).Update("plan_status", constants.PlanArchived).Error; err != nil {
		t.Fatalf("archive copy: %v", err)
	}
	second, created, err := service.Reuse(context.Background(), plan.ID, dto.ReuseDivePlanRequest{DiverProfileID: profile.ID}, reuseActor())
	if err != nil {
		t.Fatalf("reuse after archive: %v", err)
	}
	if !created || second.ID == first.ID || second.PlanCode == first.PlanCode {
		t.Fatalf("archived copy must allow a fresh draft, got id=%d code=%s created=%v", second.ID, second.PlanCode, created)
	}
}

func TestReuseRejectsInactiveProfile(t *testing.T) {
	service, db := newReuseTestService(t)
	profile, plan := seedReuseTemplate(t, db, constants.PlanApprovedTraining)
	if err := db.Model(&model.DiverProfile{}).Where("id = ?", profile.ID).Update("profile_status", "training_hold").Error; err != nil {
		t.Fatalf("hold profile: %v", err)
	}
	_, _, err := service.Reuse(context.Background(), plan.ID, dto.ReuseDivePlanRequest{DiverProfileID: profile.ID}, reuseActor())
	var appErr *util.AppError
	if !errors.As(err, &appErr) || appErr.Code != "PROFILE_NOT_ACTIVE" {
		t.Fatalf("expected PROFILE_NOT_ACTIVE, got %v", err)
	}
}
