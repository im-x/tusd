package handler_test

import (
	"github.com/im-x/tusd/pkg/filestore"
	"github.com/im-x/tusd/pkg/handler"
	"github.com/im-x/tusd/pkg/memorylocker"
)

func ExampleNewStoreComposer() {
	composer := handler.NewStoreComposer()

	fs := filestore.New("./data")
	fs.UseIn(composer)

	ml := memorylocker.New()
	ml.UseIn(composer)

	config := handler.Config{
		StoreComposer: composer,
	}

	_, _ = handler.NewHandler(config)
}
