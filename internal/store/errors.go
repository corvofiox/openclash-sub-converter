package store

import "errors"

// 哨兵错误：数据层与 API 层的错误分类契约。
//
//	ErrNotFound → API 404（资源不存在）
//	ErrInvalid  → API 400（参数/字段非法）
//	其余错误（IO/序列化等）→ API 500（内部错误，响应只给通用消息）
var (
	// ErrNotFound 表示目标资源不存在（source/template/log）。
	ErrNotFound = errors.New("not found")
	// ErrInvalid 表示入参非法（空 name/url、behavior/format 越界等）。
	ErrInvalid = errors.New("invalid")
)
