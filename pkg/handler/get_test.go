package handler_test

import (
	"context"
	"io"
	"io/ioutil"
	"net/http"
	"strings"
	"testing"

	"github.com/golang/mock/gomock"
	. "github.com/im-x/tusd/pkg/handler"
)

type closingStringReader struct {
	*strings.Reader
	closed bool
}

func (reader *closingStringReader) Close() error {
	reader.closed = true
	return nil
}

// rangeReadableUpload wraps a MockFullUpload and adds RangeReadableUpload
// support so the handler takes the native-range fast path.
type rangeReadableUpload struct {
	*MockFullUpload
	rangeReader io.ReadCloser
	rangeErr    error
	calledStart int64
	calledEnd   int64
	rangeCalled bool
}

func (u *rangeReadableUpload) GetReaderRange(_ context.Context, start, end int64) (io.ReadCloser, error) {
	u.rangeCalled = true
	u.calledStart = start
	u.calledEnd = end
	return u.rangeReader, u.rangeErr
}

func TestGet(t *testing.T) {
	SubTest(t, "Download", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		reader := &closingStringReader{
			Reader: strings.NewReader("hello"),
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		locker := NewMockFullLocker(ctrl)
		lock := NewMockFullLock(ctrl)
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			locker.EXPECT().NewLock("yes").Return(lock, nil),
			lock.EXPECT().Lock().Return(nil),
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 5,
				Size:   20,
				MetaData: map[string]string{
					"filename": "file.jpg\"evil",
					"filetype": "image/jpeg",
				},
			}, nil),
			upload.EXPECT().GetReader(context.Background()).Return(reader, nil),
			lock.EXPECT().Unlock().Return(nil),
		)

		composer = NewStoreComposer()
		composer.UseCore(store)
		composer.UseLocker(locker)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ResHeader: map[string]string{
				"Content-Length":      "5",
				"Content-Type":        "image/jpeg",
				"Content-Disposition": `inline;filename="file.jpg\"evil"`,
				"Accept-Ranges":       "bytes",
			},
			Code:    http.StatusOK,
			ResBody: "hello",
		}).Run(handler, t)

		if !reader.closed {
			t.Error("expected reader to be closed")
		}
	})

	SubTest(t, "EmptyDownload", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 0,
			}, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ResHeader: map[string]string{
				"Content-Length":      "0",
				"Content-Disposition": `attachment`,
				"Accept-Ranges":       "bytes",
			},
			Code:    http.StatusNoContent,
			ResBody: "",
		}).Run(handler, t)
	})

	SubTest(t, "InvalidFileType", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 0,
				MetaData: map[string]string{
					"filetype": "non-a-valid-mime-type",
				},
			}, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ResHeader: map[string]string{
				"Content-Length":      "0",
				"Content-Type":        "application/octet-stream",
				"Content-Disposition": `attachment`,
			},
			Code:    http.StatusNoContent,
			ResBody: "",
		}).Run(handler, t)
	})

	SubTest(t, "NotWhitelistedFileType", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 0,
				MetaData: map[string]string{
					"filetype": "application/vnd.openxmlformats-officedocument.wordprocessingml.document.v1",
					"filename": "invoice.docx",
				},
			}, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ResHeader: map[string]string{
				"Content-Length":      "0",
				"Content-Type":        "application/vnd.openxmlformats-officedocument.wordprocessingml.document.v1",
				"Content-Disposition": `attachment;filename="invoice.docx"`,
			},
			Code:    http.StatusNoContent,
			ResBody: "",
		}).Run(handler, t)
	})

	SubTest(t, "RangeExplicitFallback", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		reader := &closingStringReader{
			Reader: strings.NewReader("hello world!"),
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 12,
				MetaData: map[string]string{
					"filetype": "text/plain",
				},
			}, nil),
			upload.EXPECT().GetReader(context.Background()).Return(reader, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ReqHeader: map[string]string{
				"Range": "bytes=2-4",
			},
			ResHeader: map[string]string{
				"Content-Length": "3",
				"Content-Range":  "bytes 2-4/12",
				"Accept-Ranges":  "bytes",
			},
			Code:    http.StatusPartialContent,
			ResBody: "llo",
		}).Run(handler, t)

		if !reader.closed {
			t.Error("expected reader to be closed")
		}
	})

	SubTest(t, "RangeFirstByte", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		reader := &closingStringReader{
			Reader: strings.NewReader("hello"),
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 5,
				MetaData: map[string]string{
					"filetype": "text/plain",
				},
			}, nil),
			upload.EXPECT().GetReader(context.Background()).Return(reader, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ReqHeader: map[string]string{
				"Range": "bytes=0-0",
			},
			ResHeader: map[string]string{
				"Content-Length": "1",
				"Content-Range":  "bytes 0-0/5",
			},
			Code:    http.StatusPartialContent,
			ResBody: "h",
		}).Run(handler, t)
	})

	SubTest(t, "RangeOpenEnd", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		reader := &closingStringReader{
			Reader: strings.NewReader("hello"),
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 5,
				MetaData: map[string]string{
					"filetype": "text/plain",
				},
			}, nil),
			upload.EXPECT().GetReader(context.Background()).Return(reader, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ReqHeader: map[string]string{
				"Range": "bytes=3-",
			},
			ResHeader: map[string]string{
				"Content-Length": "2",
				"Content-Range":  "bytes 3-4/5",
			},
			Code:    http.StatusPartialContent,
			ResBody: "lo",
		}).Run(handler, t)
	})

	SubTest(t, "RangeSuffix", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		reader := &closingStringReader{
			Reader: strings.NewReader("hello"),
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 5,
				MetaData: map[string]string{
					"filetype": "text/plain",
				},
			}, nil),
			upload.EXPECT().GetReader(context.Background()).Return(reader, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ReqHeader: map[string]string{
				"Range": "bytes=-2",
			},
			ResHeader: map[string]string{
				"Content-Length": "2",
				"Content-Range":  "bytes 3-4/5",
			},
			Code:    http.StatusPartialContent,
			ResBody: "lo",
		}).Run(handler, t)
	})

	SubTest(t, "RangeFullFileReturns200", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		reader := &closingStringReader{
			Reader: strings.NewReader("hello"),
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 5,
				MetaData: map[string]string{
					"filetype": "text/plain",
				},
			}, nil),
			upload.EXPECT().GetReader(context.Background()).Return(reader, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ReqHeader: map[string]string{
				"Range": "bytes=0-",
			},
			ResHeader: map[string]string{
				"Content-Length": "5",
			},
			Code:    http.StatusOK,
			ResBody: "hello",
		}).Run(handler, t)
	})

	SubTest(t, "RangeNotSatisfiable", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 5,
				MetaData: map[string]string{
					"filetype": "text/plain",
				},
			}, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ReqHeader: map[string]string{
				"Range": "bytes=10-20",
			},
			ResHeader: map[string]string{
				"Content-Range": "bytes */5",
			},
			Code: http.StatusRequestedRangeNotSatisfiable,
		}).Run(handler, t)
	})

	SubTest(t, "RangeInvalidSyntax", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 5,
				MetaData: map[string]string{
					"filetype": "text/plain",
				},
			}, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ReqHeader: map[string]string{
				"Range": "invalid",
			},
			ResHeader: map[string]string{
				"Content-Range": "bytes */5",
			},
			Code: http.StatusRequestedRangeNotSatisfiable,
		}).Run(handler, t)
	})

	SubTest(t, "RangeMultiRangeUnsupported", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 10,
				MetaData: map[string]string{
					"filetype": "text/plain",
				},
			}, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ReqHeader: map[string]string{
				"Range": "bytes=0-1,3-4",
			},
			Code: http.StatusRequestedRangeNotSatisfiable,
		}).Run(handler, t)
	})

	SubTest(t, "AttachmentHasAcceptRanges", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		reader := &closingStringReader{
			Reader: strings.NewReader("hello"),
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 5,
				MetaData: map[string]string{
					"filetype": "application/pdf",
					"filename": "doc.pdf",
				},
			}, nil),
			upload.EXPECT().GetReader(context.Background()).Return(reader, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ResHeader: map[string]string{
				"Content-Disposition": `attachment;filename="doc.pdf"`,
				"Accept-Ranges":       "bytes",
			},
			Code:    http.StatusOK,
			ResBody: "hello",
		}).Run(handler, t)
	})

	SubTest(t, "RangeNativeReadPath", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		baseUpload := NewMockFullUpload(ctrl)

		rangeBody := ioutil.NopCloser(strings.NewReader("llo"))
		rru := &rangeReadableUpload{
			MockFullUpload: baseUpload,
			rangeReader:    rangeBody,
		}

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(rru, nil),
			baseUpload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 12,
				MetaData: map[string]string{
					"filetype": "text/plain",
				},
			}, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ReqHeader: map[string]string{
				"Range": "bytes=2-4",
			},
			ResHeader: map[string]string{
				"Content-Length": "3",
				"Content-Range":  "bytes 2-4/12",
			},
			Code:    http.StatusPartialContent,
			ResBody: "llo",
		}).Run(handler, t)

		if !rru.rangeCalled {
			t.Error("expected GetReaderRange to be called")
		}
		if rru.calledStart != 2 || rru.calledEnd != 4 {
			t.Errorf("expected GetReaderRange(2,4), got (%d,%d)", rru.calledStart, rru.calledEnd)
		}
	})

	SubTest(t, "FiletypeShorthandMp4", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		reader := &closingStringReader{
			Reader: strings.NewReader("video"),
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 5,
				MetaData: map[string]string{
					"filetype": "mp4",
					"filename": "clip.mp4",
				},
			}, nil),
			upload.EXPECT().GetReader(context.Background()).Return(reader, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ResHeader: map[string]string{
				"Content-Type":        "video/mp4",
				"Content-Disposition": `inline;filename="clip.mp4"`,
				"Accept-Ranges":       "bytes",
			},
			Code:    http.StatusOK,
			ResBody: "video",
		}).Run(handler, t)
	})

	SubTest(t, "FiletypeShorthandMp4Uppercase", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		reader := &closingStringReader{
			Reader: strings.NewReader("data"),
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 4,
				MetaData: map[string]string{
					"filetype": "MP4",
				},
			}, nil),
			upload.EXPECT().GetReader(context.Background()).Return(reader, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ResHeader: map[string]string{
				"Content-Type":        "video/mp4",
				"Content-Disposition": "inline",
				"Accept-Ranges":       "bytes",
			},
			Code:    http.StatusOK,
			ResBody: "data",
		}).Run(handler, t)
	})

	SubTest(t, "FiletypeShorthandMp4WithSpaces", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		reader := &closingStringReader{
			Reader: strings.NewReader("x"),
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 1,
				MetaData: map[string]string{
					"filetype": "  mp4  ",
				},
			}, nil),
			upload.EXPECT().GetReader(context.Background()).Return(reader, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ResHeader: map[string]string{
				"Content-Type":        "video/mp4",
				"Content-Disposition": "inline",
			},
			Code:    http.StatusOK,
			ResBody: "x",
		}).Run(handler, t)
	})

	SubTest(t, "FiletypeShorthandUnknownStillAttachment", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		reader := &closingStringReader{
			Reader: strings.NewReader("bin"),
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 3,
				MetaData: map[string]string{
					"filetype": "xyz",
				},
			}, nil),
			upload.EXPECT().GetReader(context.Background()).Return(reader, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ResHeader: map[string]string{
				"Content-Type":        "application/octet-stream",
				"Content-Disposition": "attachment",
			},
			Code:    http.StatusOK,
			ResBody: "bin",
		}).Run(handler, t)
	})

	SubTest(t, "FiletypeShorthandJpg", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		reader := &closingStringReader{
			Reader: strings.NewReader("img"),
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 3,
				MetaData: map[string]string{
					"filetype": "jpg",
					"filename": "photo.jpg",
				},
			}, nil),
			upload.EXPECT().GetReader(context.Background()).Return(reader, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ResHeader: map[string]string{
				"Content-Type":        "image/jpeg",
				"Content-Disposition": `inline;filename="photo.jpg"`,
			},
			Code:    http.StatusOK,
			ResBody: "img",
		}).Run(handler, t)
	})

	SubTest(t, "FiletypeShorthandPng", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		reader := &closingStringReader{
			Reader: strings.NewReader("x"),
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 1,
				MetaData: map[string]string{
					"filetype": "png",
				},
			}, nil),
			upload.EXPECT().GetReader(context.Background()).Return(reader, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes",
			ResHeader: map[string]string{
				"Content-Type":        "image/png",
				"Content-Disposition": "inline",
			},
			Code:    http.StatusOK,
			ResBody: "x",
		}).Run(handler, t)
	})

	SubTest(t, "FiletypeShorthandWebAssets", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		tests := []struct {
			filetype    string
			contentType string
		}{
			{filetype: "html", contentType: "text/html"},
			{filetype: "css", contentType: "text/css"},
			{filetype: "js", contentType: "application/javascript"},
		}

		for _, tt := range tests {
			t.Run(tt.filetype, func(t *testing.T) {
				reader := &closingStringReader{
					Reader: strings.NewReader("asset"),
				}

				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				upload := NewMockFullUpload(ctrl)

				gomock.InOrder(
					store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
					upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
						Offset: 5,
						MetaData: map[string]string{
							"filetype": tt.filetype,
						},
					}, nil),
					upload.EXPECT().GetReader(context.Background()).Return(reader, nil),
				)

				handler, _ := NewHandler(Config{
					StoreComposer: composer,
				})

				(&httpTest{
					Method: "GET",
					URL:    "yes",
					ResHeader: map[string]string{
						"Content-Type":        tt.contentType,
						"Content-Disposition": "attachment",
					},
					Code:    http.StatusOK,
					ResBody: "asset",
				}).Run(handler, t)
			})
		}
	})

	SubTest(t, "InlineQueryForcesAttachmentFileTypeInline", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		reader := &closingStringReader{
			Reader: strings.NewReader("<html></html>"),
		}

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		upload := NewMockFullUpload(ctrl)

		gomock.InOrder(
			store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
			upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
				Offset: 13,
				MetaData: map[string]string{
					"filetype": "html",
					"filename": "index.html",
				},
			}, nil),
			upload.EXPECT().GetReader(context.Background()).Return(reader, nil),
		)

		handler, _ := NewHandler(Config{
			StoreComposer: composer,
		})

		(&httpTest{
			Method: "GET",
			URL:    "yes?inline=true",
			ResHeader: map[string]string{
				"Content-Type":        "text/html",
				"Content-Disposition": `inline;filename="index.html"`,
			},
			Code:    http.StatusOK,
			ResBody: "<html></html>",
		}).Run(handler, t)
	})

	SubTest(t, "ParameterizedMimeTypeReturnsBaseContentType", func(t *testing.T, store *MockFullDataStore, composer *StoreComposer) {
		tests := []struct {
			name               string
			url                string
			contentDisposition string
		}{
			{
				name:               "default attachment",
				url:                "yes",
				contentDisposition: `attachment;filename="style.css"`,
			},
			{
				name:               "inline query",
				url:                "yes?inline=true",
				contentDisposition: `inline;filename="style.css"`,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				reader := &closingStringReader{
					Reader: strings.NewReader("body"),
				}

				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				upload := NewMockFullUpload(ctrl)

				gomock.InOrder(
					store.EXPECT().GetUpload(context.Background(), "yes").Return(upload, nil),
					upload.EXPECT().GetInfo(context.Background()).Return(FileInfo{
						Offset: 4,
						MetaData: map[string]string{
							"filetype": "text/css; charset=utf-8",
							"filename": "style.css",
						},
					}, nil),
					upload.EXPECT().GetReader(context.Background()).Return(reader, nil),
				)

				handler, _ := NewHandler(Config{
					StoreComposer: composer,
				})

				(&httpTest{
					Method: "GET",
					URL:    tt.url,
					ResHeader: map[string]string{
						"Content-Type":        "text/css",
						"Content-Disposition": tt.contentDisposition,
					},
					Code:    http.StatusOK,
					ResBody: "body",
				}).Run(handler, t)
			})
		}
	})
}
