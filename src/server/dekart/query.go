package dekart

import (
	"context"
	"crypto/sha1"
	"dekart/src/proto"
	"dekart/src/server/job"
	"dekart/src/server/user"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CreateQuery in Report
func (s Server) CreateQuery(ctx context.Context, req *proto.CreateQueryRequest) (*proto.CreateQueryResponse, error) {
	claims := user.GetClaims(ctx)
	if claims == nil {
		return nil, Unauthenticated
	}
	if req.Query == nil {
		return nil, status.Errorf(codes.InvalidArgument, "req.Query == nil")
	}

	id := newUUID()
	result, err := s.db.ExecContext(ctx,
		`insert into queries (id, report_id, query_text)
		select
			$1 as id,
			id as report_id,
			'' as query_text
		from reports
		where id=$2 and not archived and author_email=$3 limit 1
		`,
		id,
		req.Query.ReportId,
		claims.Email,
	)
	if err != nil {
		log.Err(err).Send()
		return nil, status.Error(codes.Internal, err.Error())
	}

	affectedRows, err := result.RowsAffected()
	if err != nil {
		log.Err(err).Send()
		return nil, status.Error(codes.Internal, err.Error())
	}

	if affectedRows == 0 {
		err := fmt.Errorf("report=%s, author_email=%s not found", req.Query.ReportId, claims.Email)
		log.Warn().Err(err).Send()
		return nil, status.Errorf(codes.NotFound, err.Error())
	}
	go s.storeQuery(req.Query.ReportId, id, req.Query.QueryText, "")
	s.reportStreams.Ping(req.Query.ReportId)

	res := &proto.CreateQueryResponse{
		Query: &proto.Query{
			Id:        id,
			ReportId:  req.Query.ReportId,
			QueryText: req.Query.QueryText,
		},
	}

	return res, nil
}

func (s Server) getReportID(ctx context.Context, queryID string, email string) (*string, error) {
	queryRows, err := s.db.QueryContext(ctx,
		`select report_id from queries
		where id=$1 and report_id in (select report_id from reports where author_email=$2)
		limit 1`,
		queryID,
		email,
	)
	if err != nil {
		return nil, err
	}
	defer queryRows.Close()
	var reportID string
	for queryRows.Next() {
		err := queryRows.Scan(&reportID)
		if err != nil {
			return nil, err
		}
	}
	if reportID == "" {
		return nil, nil
	}
	return &reportID, nil
}

// queryWasNotUpdated was not updated because it was changed
type queryWasNotUpdated struct{}

func (e *queryWasNotUpdated) Error() string {
	return "query was not updated"
}

func (s Server) storeQuerySync(ctx context.Context, queryID string, queryText string, prevQuerySourceId string) error {
	h := sha1.New()
	queryTextByte := []byte(queryText)
	h.Write(queryTextByte)
	newQuerySourceId := fmt.Sprintf("%x", h.Sum(nil))

	storageWriter := s.bucket.Writer(ctx, fmt.Sprintf("%s.sql", newQuerySourceId))

	_, err := storageWriter.Write(queryTextByte)
	if err != nil {
		log.Err(err).Msg("Error writing query_text to storage")
		storageWriter.Close()
		return err
	}
	err = storageWriter.Close()
	if err != nil {
		log.Err(err).Msg("Error writing query_text to storage")
		return err
	}

	result, err := s.db.ExecContext(ctx,
		`update queries set query_source_id=$1, query_source=$2 where id=$3 and query_source_id=$4`,
		newQuerySourceId,
		proto.Query_QUERY_SOURCE_STORAGE,
		queryID,
		prevQuerySourceId,
	)
	if err != nil {
		return err
	}
	affectedRows, _ := result.RowsAffected()
	if affectedRows == 0 {
		return &queryWasNotUpdated{}
	}
	return nil
}

func (s Server) storeQuery(reportID string, queryID string, queryText string, prevQuerySourceId string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	err := s.storeQuerySync(ctx, queryID, queryText, prevQuerySourceId)
	if _, ok := err.(*queryWasNotUpdated); ok {
		log.Warn().Msg("Query text not updated")
		return
	} else if err != nil {
		log.Err(err).Msg("Error updating query text")
		return
	}
	log.Debug().Msg("Query text updated in storage")
	s.reportStreams.Ping(reportID)
}

// UpdateQuery by id implementation
func (s Server) UpdateQuery(ctx context.Context, req *proto.UpdateQueryRequest) (*proto.UpdateQueryResponse, error) {
	claims := user.GetClaims(ctx)
	if claims == nil {
		return nil, Unauthenticated
	}
	if req.Query == nil {
		return nil, status.Errorf(codes.InvalidArgument, "req.Query == nil")
	}

	reportID, err := s.getReportID(ctx, req.Query.Id, claims.Email)

	if err != nil {
		log.Err(err).Send()
		return nil, status.Error(codes.Internal, err.Error())
	}

	if reportID == nil {
		err := fmt.Errorf("query not found id:%s", req.Query.Id)
		log.Warn().Err(err).Send()
		return nil, status.Error(codes.NotFound, err.Error())
	}

	go s.storeQuery(*reportID, req.Query.Id, req.Query.QueryText, req.Query.QuerySourceId)

	res := &proto.UpdateQueryResponse{
		Query: &proto.Query{
			Id: req.Query.Id,
		},
	}

	return res, nil
}

func (s Server) updateJobStatus(job *job.Job) {
	for {
		select {
		case status := <-job.Status:
			log.Debug().Int32("status", status).Msg("job status")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			var err error
			if status == int32(proto.Query_JOB_STATUS_RUNNING) {
				_, err = s.db.ExecContext(
					ctx,
					`update queries set
						job_status = $1,
						job_error = $3,
						job_result_id = $4,
						job_started = CURRENT_TIMESTAMP,
						total_rows = 0,
						bytes_processed = 0,
						result_size = 0
					where id  = $2`,
					status,
					job.QueryID,
					job.Err(),
					job.GetResultID(),
				)

			} else {
				_, err = s.db.ExecContext(
					ctx,
					`update queries set
						job_status = $1,
						job_error = $3,
						job_result_id = $4,
						total_rows = $5,
						bytes_processed = $6,
						result_size = $7
					where id  = $2`,
					status,
					job.QueryID,
					job.Err(),
					job.GetResultID(),
					job.GetTotalRows(),
					job.GetProcessedBytes(),
					job.GetResultSize(),
				)
			}
			cancel()
			if err != nil {
				log.Fatal().Err(err).Send()
			}
			s.reportStreams.Ping(job.ReportID)
		case <-job.Ctx.Done():
			return
		}
	}
}

// RunQuery job against database
func (s Server) RunQuery(ctx context.Context, req *proto.RunQueryRequest) (*proto.RunQueryResponse, error) {
	claims := user.GetClaims(ctx)
	if claims == nil {
		return nil, Unauthenticated
	}
	queriesRows, err := s.db.QueryContext(ctx,
		`select
			report_id,
			query_source_id
		from queries where id=$1 and report_id in (select report_id from reports where author_email=$2) limit 1`,
		req.QueryId,
		claims.Email,
	)

	if err != nil {
		log.Err(err).Send()
		return nil, status.Error(codes.Internal, err.Error())
	}
	defer queriesRows.Close()
	var reportID string
	var prevQuerySourceId string
	for queriesRows.Next() {
		err := queriesRows.Scan(&reportID, &prevQuerySourceId)
		if err != nil {
			log.Err(err).Send()
			return nil, status.Error(codes.Internal, err.Error())
		}
	}

	if reportID == "" {
		err := fmt.Errorf("query not found id:%s", req.QueryId)
		log.Warn().Err(err).Send()
		return nil, status.Error(codes.NotFound, err.Error())
	}

	err = s.storeQuerySync(ctx, req.QueryId, req.QueryText, prevQuerySourceId)

	if err != nil {
		code := codes.Internal
		if _, ok := err.(*queryWasNotUpdated); ok {
			code = codes.Canceled
			log.Warn().Err(err).Send()
		} else {
			log.Error().Err(err).Send()
		}
		return nil, status.Error(code, err.Error())
	}

	job, err := s.jobs.NewJob(reportID, req.QueryId)
	if err != nil {
		log.Error().Err(err).Send()
		return nil, status.Error(codes.Internal, err.Error())
	}
	storageWriter := s.bucket.Writer(job.Ctx, fmt.Sprintf("%s.csv", job.ID))
	go s.updateJobStatus(job)
	err = job.Run(req.QueryText, storageWriter)
	if err != nil {
		log.Err(err).Send()
		return nil, status.Error(codes.Internal, err.Error())
	}
	res := &proto.RunQueryResponse{}
	return res, nil
}

func (s Server) RemoveQuery(ctx context.Context, req *proto.RemoveQueryRequest) (*proto.RemoveQueryResponse, error) {
	claims := user.GetClaims(ctx)
	if claims == nil {
		return nil, Unauthenticated
	}
	_, err := uuid.Parse(req.QueryId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, err.Error())
	}

	reportID, err := s.getReportID(ctx, req.QueryId, claims.Email)

	if err != nil {
		log.Err(err).Send()
		return nil, status.Error(codes.Internal, err.Error())
	}

	if reportID == nil {
		err := fmt.Errorf("Query not found id:%s", req.QueryId)
		log.Warn().Err(err).Send()
		return nil, status.Error(codes.NotFound, err.Error())
	}

	s.jobs.Cancel(req.QueryId)

	_, err = s.db.ExecContext(ctx,
		`delete from queries where id=$1`,
		req.QueryId,
	)
	if err != nil {
		log.Err(err).Send()
		return nil, status.Error(codes.Internal, err.Error())
	}

	s.reportStreams.Ping(*reportID)

	return &proto.RemoveQueryResponse{}, nil

}

// CancelQuery jobs
func (s Server) CancelQuery(ctx context.Context, req *proto.CancelQueryRequest) (*proto.CancelQueryResponse, error) {
	claims := user.GetClaims(ctx)
	if claims == nil {
		return nil, Unauthenticated
	}
	_, err := uuid.Parse(req.QueryId)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, err.Error())
	}
	queriesRows, err := s.db.QueryContext(ctx,
		`select
			report_id
		from queries
		where id=$1 and report_id in (select report_id from reports where author_email=$2) limit 1`,
		req.QueryId,
		claims.Email,
	)
	if err != nil {
		log.Err(err).Send()
		return nil, status.Error(codes.Internal, err.Error())
	}
	defer queriesRows.Close()
	var reportID string
	for queriesRows.Next() {
		err := queriesRows.Scan(&reportID)
		if err != nil {
			log.Err(err).Send()
			return nil, status.Error(codes.Internal, err.Error())
		}
	}
	if reportID == "" {
		log.Warn().Str("QueryId", req.QueryId).Msg("Query not found")
		return nil, status.Error(codes.NotFound, err.Error())
	}
	s.jobs.Cancel(req.QueryId)
	return &proto.CancelQueryResponse{}, nil
}
