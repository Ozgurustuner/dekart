package job

import (
	"io"
	"sync"

	"context"

	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/rs/zerolog"
)

// Job of quering db, concurency safe
type Job struct {
	ID                  string
	QueryID             string
	ReportID            string
	Ctx                 context.Context
	cancel              context.CancelFunc
	athenaQuery         *AthenaQuery
	Status              chan int32
	err                 string
	totalRows           int64
	processedBytes      int64
	resultSize          int64
	resultID            *string
	storageWriter       io.WriteCloser
	mutex               sync.Mutex
	logger              zerolog.Logger
	maxReadStreamsCount int32
	maxBytesBilled      int64
	outputBucket        string
	region              string
	awsSession          *session.Session
}

// Err of job
func (job *Job) Err() string {
	job.mutex.Lock()
	defer job.mutex.Unlock()
	return job.err
}

// GetResultSize of the job
func (job *Job) GetResultSize() int64 {
	job.mutex.Lock()
	defer job.mutex.Unlock()
	return job.resultSize
}

// GetResultID for the job; nil means results not yet saved
func (job *Job) GetResultID() *string {
	job.mutex.Lock()
	defer job.mutex.Unlock()
	return job.resultID
}

// GetTotalRows in result
func (job *Job) GetTotalRows() int64 {
	job.mutex.Lock()
	defer job.mutex.Unlock()
	return job.totalRows
}

// GetProcessedBytes in result
func (job *Job) GetProcessedBytes() int64 {
	job.mutex.Lock()
	defer job.mutex.Unlock()
	return job.processedBytes
}

// var contextCancelledRe = regexp.MustCompile(`context canceled`)
// var orderByRe = regexp.MustCompile(`(?ims)order[\s]+by`)

// func (job *Job) close(storageWriter io.WriteCloser, csvWriter *csv.Writer) {
// 	csvWriter.Flush()
// 	err := storageWriter.Close()
// 	if err != nil {
// 		// maybe we should not close when context is canceled in job.write()
// 		if err == context.Canceled {
// 			return
// 		}
// 		if contextCancelledRe.MatchString(err.Error()) {
// 			return
// 		}
// 		job.logger.Err(err).Send()
// 		job.cancelWithError(err)
// 		return
// 	}
// 	job.logger.Debug().Msg("Writing Done")
// 	job.mutex.Lock()
// 	// TODO: use bool done or better new status values
// 	job.resultID = &job.ID
// 	job.mutex.Unlock()
// 	job.Status <- int32(proto.Query_JOB_STATUS_DONE)
// 	job.cancel()
// }

// func (job *Job) setJobStats(queryStatus *bigquery.JobStatus, table *bigquery.Table) error {
// 	tableMetadata, err := table.Metadata(job.Ctx)
// 	if err != nil {
// 		return err
// 	}
// 	job.mutex.Lock()
// 	defer job.mutex.Unlock()
// 	if queryStatus.Statistics != nil {
// 		job.processedBytes = queryStatus.Statistics.TotalBytesProcessed
// 	}
// 	job.totalRows = int64(tableMetadata.NumRows)
// 	return nil
// }

// // write csv rows to storage
// func (job *Job) write(csvRows chan []string) {
// 	csvWriter := csv.NewWriter(job.storageWriter)
// 	for {
// 		csvRow, more := <-csvRows
// 		if !more {
// 			break
// 		}
// 		err := csvWriter.Write(csvRow)
// 		if err == context.Canceled {
// 			break
// 		}
// 		if err != nil {
// 			job.logger.Err(err).Send()
// 			job.cancelWithError(err)
// 			break
// 		}
// 	}
// 	job.close(job.storageWriter, csvWriter)
// }

// func (job *Job) cancelWithError(err error) {
// 	job.mutex.Lock()
// 	job.err = err.Error()
// 	job.mutex.Unlock()
// 	job.Status <- 0
// 	job.cancel()
// }

// type AvroSchema struct {
// 	Fields []struct {
// 		Name string `json:"name"`
// 	} `json:"fields"`
// }

// func (job *Job) proccessApiErrors(err error) {
// 	if apiError, ok := err.(*googleapi.Error); ok {
// 		for _, e := range apiError.Errors {
// 			if e.Reason == "bytesBilledLimitExceeded" {
// 				job.logger.Warn().Str(
// 					"DEKART_BIGQUERY_MAX_BYTES_BILLED", os.Getenv("DEKART_BIGQUERY_MAX_BYTES_BILLED"),
// 				).Msg(e.Message)
// 			}
// 		}
// 	}
// }

// func (job *Job) getResultTable() (*bigquery.Table, error) {
// 	jobConfig, err := job.bigqueryJob.Config()
// 	if err != nil {
// 		return nil, err
// 	}
// 	jobConfigVal := reflect.ValueOf(jobConfig).Elem()
// 	table, ok := jobConfigVal.FieldByName("Dst").Interface().(*bigquery.Table)
// 	if !ok {
// 		err := fmt.Errorf("cannot get destination table from job config")
// 		job.logger.Error().Err(err).Str("jobConfig", fmt.Sprintf("%v+", jobConfig)).Send()
// 		return nil, err
// 	}
// 	return table, nil
// }

// func (job *Job) wait() {
// 	queryStatus, err := job.bigqueryJob.Wait(job.Ctx)
// 	if err == context.Canceled {
// 		return
// 	}
// 	if err != nil {
// 		job.proccessApiErrors(err)
// 		job.cancelWithError(err)
// 		return
// 	}
// 	if queryStatus == nil {
// 		job.logger.Fatal().Msgf("queryStatus == nil")
// 	}
// 	if err := queryStatus.Err(); err != nil {
// 		job.cancelWithError(err)
// 		return
// 	}

// 	table, err := job.getResultTable()
// 	if err != nil {
// 		job.cancelWithError(err)
// 		return
// 	}

// 	err = job.setJobStats(queryStatus, table)
// 	if err != nil {
// 		job.cancelWithError(err)
// 		return
// 	}

// 	// TODO: reading result as separate state
// 	job.Status <- int32(queryStatus.State)

// 	csvRows := make(chan []string, job.totalRows)
// 	errors := make(chan error)

// 	// read table rows into csvRows
// 	go Read(
// 		job.Ctx,
// 		errors,
// 		csvRows,
// 		table,
// 		job.logger,
// 		job.maxReadStreamsCount,
// 	)

// 	// write csvRows to storage
// 	go job.write(csvRows)

// 	// wait for errors
// 	err = <-errors
// 	if err != nil {
// 		job.cancelWithError(err)
// 		return
// 	}
// 	job.logger.Debug().Msg("Job Wait Done")
// }

// func (job *Job) setMaxReadStreamsCount(queryText string) {
// 	job.mutex.Lock()
// 	defer job.mutex.Unlock()
// 	if orderByRe.MatchString(queryText) {
// 		job.maxReadStreamsCount = 1 // keep order of items
// 	} else {
// 		job.maxReadStreamsCount = 10
// 	}
// }

func (job *Job) Run(queryText string, storageWriter io.WriteCloser) error {
	var err error
	var q AthenaQuery
	q.inputParams = &InputParams{
		AwsSession:   job.awsSession,
		QueryString:  &queryText,
		Region:       &job.region,
		OutputBucket: &job.outputBucket,
	}

	// create query
	q.createQuery()

	// create client
	err = q.createClient()
	if err != nil {
		return err
	}

	// start query
	err = q.startQuery()
	if err != nil {
		return err
	}

	return nil
}

// // Run implementation
// func (job *Job) Run(queryText string, storageWriter io.WriteCloser, athenaDb *sql.DB) error {
// 	job.logger.Debug().Msg("Run Athena Job")
// 	// client, err := bigquery.NewClient(job.Ctx, os.Getenv("DEKART_BIGQUERY_PROJECT_ID"))
// 	// if err != nil {
// 	// 	job.cancel()
// 	// 	return err
// 	// }
// 	// query := client.Query(queryText)
// 	// query.MaxBytesBilled = job.maxBytesBilled

// 	// job.setMaxReadStreamsCount(queryText)

// 	// bigqueryJob, err := query.Run(job.Ctx)
// 	// if err != nil {
// 	// 	job.cancel()
// 	// 	return err
// 	// }
// 	// job.mutex.Lock()
// 	// job.bigqueryJob = bigqueryJob
// 	// job.storageWriter = storageWriter
// 	// job.mutex.Unlock()
// 	// job.Status <- int32(proto.Query_JOB_STATUS_RUNNING)
// 	// job.logger.Debug().Msg("Waiting for results")
// 	// go job.wait()

// 	// res, err := athenaDb.QueryContext(job.Ctx, queryText)
// 	// if err != nil {
// 	// 	job.cancelWithError(err)
// 	// 	return fmt.Errorf("could not execute query for the job: %w", err)
// 	// }

// 	return nil
// }

// type SQLStore interface {
// 	Query(ctx, query) []map[string]interface{}
// }

// type Queryer interface {
// 	Query(query string, args ...interface{}) (*sql.Rows, error)
// }

// func Select(db Queryer, query string, args ...interface{}) ([]map[string]interface{}, error) {
// 	rows, err := db.Query(query, args...)
// 	if err != nil {
// 		return nil, err
// 	}
// 	return SelectScan(rows)
// }

// func SelectScan(rows *sql.Rows) ([]map[string]interface{}, error) {
// 	defer rows.Close()

// 	columns, err := rows.Columns()
// 	if err != nil {
// 		return nil, err
// 	}
// 	numColumns := len(columns)

// 	values := make([]interface{}, numColumns)
// 	for i := range values {
// 		values[i] = new(interface{})
// 	}

// 	var results []map[string]interface{}
// 	for rows.Next() {
// 		if err := rows.Scan(values...); err != nil {
// 			return nil, err
// 		}

// 		dest := make(map[string]interface{}, numColumns)
// 		for i, column := range columns {
// 			dest[column] = *(values[i].(*interface{}))
// 		}
// 		results = append(results, dest)
// 	}

// 	if err := rows.Err(); err != nil {
// 		return nil, err
// 	}
// 	return results, nil
// }

// type MyJob struct {
// 	ID             string
// 	ReportID       string
// 	Ctx            context.Context
// 	cancel         context.CancelFunc
// 	Status         chan int32
// 	err            string
// 	totalRows      int64
// 	processedBytes int64
// 	resultSize     int64

// 	logger              zerolog.Logger
// 	maxReadStreamsCount int32
// 	maxBytesBilled      int64
// }

// JobMangare {
// 	jobs map[int64]job
// }
