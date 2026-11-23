package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

const maxJobLogRead = 256 << 20

type tailBuffer struct {
	keep int
	data []byte
}

func (t *tailBuffer) Write(chunk []byte) (int, error) {
	t.data = append(t.data, chunk...)
	if len(t.data) > 2*t.keep {
		t.data = append(t.data[:0], t.data[len(t.data)-t.keep:]...)
	}
	return len(chunk), nil
}

func (t *tailBuffer) bytes() []byte {
	if len(t.data) <= t.keep {
		return t.data
	}
	return t.data[len(t.data)-t.keep:]
}

func readTail(body io.Reader, keep int, limit int64) ([]byte, error) {
	buffer := &tailBuffer{keep: keep}
	read, err := io.Copy(buffer, io.LimitReader(body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read job log: %w", err)
	}
	if read > limit {
		return nil, errJobLogTooLarge
	}
	return buffer.bytes(), nil
}

func (f *forge) JobLog(ctx context.Context, job contracts.JobRef, maxBytes int) ([]byte, error) {
	if err := f.checkRepo(job.Repo()); err != nil {
		return nil, err
	}
	if err := contracts.ValidateLogLimit(maxBytes); err != nil {
		return nil, err
	}
	spec := request{method: http.MethodGet, target: f.repoURL(job.Repo(), nil, "actions", "jobs", strconv.FormatInt(job.ID(), 10), "logs")}
	var tail []byte
	err := f.exchange(ctx, spec, func(_ contracts.ForgeToken, resp *http.Response) error {
		var err error
		tail, err = readTail(resp.Body, maxBytes, maxJobLogRead)
		return err
	})
	return tail, err
}
