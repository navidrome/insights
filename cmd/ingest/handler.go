package main

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/navidrome/insights/internal/store"
	"github.com/navidrome/navidrome/core/metrics/insights"
)

func handler(w *store.Writer) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		var data insights.Data

		err := decodeJSONBody(rw, r, &data)
		if err != nil {
			var mr *malformedRequest
			if errors.As(err, &mr) {
				http.Error(rw, mr.msg, mr.status)
			} else {
				log.Printf("error decoding payload: %s", err.Error()) //#nosec G706 -- error message is safe
				http.Error(rw, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			}
			return
		}

		if err := w.Append(data, time.Now()); err != nil {
			log.Printf("Error handling request: %s", err.Error()) //#nosec G706 -- error message is safe
			rw.WriteHeader(http.StatusInternalServerError)
			return
		}

		rw.WriteHeader(http.StatusOK)
	}
}

// healthzHandler reports that the process is up and accepting reports.
func healthzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	}
}
