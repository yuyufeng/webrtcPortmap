package terminal

// ringBuffer 是一个固定容量的字节环形缓冲，用于保存终端最近的输出，
// 以便控制端重连后回放“当前屏幕之前”的历史内容。超出容量时丢弃最旧字节。
type ringBuffer struct {
	buf   []byte
	size  int
	start int // 最旧字节的下标
	count int // 有效字节数（<= size）
}

func newRingBuffer(size int) *ringBuffer {
	if size <= 0 {
		size = 1
	}
	return &ringBuffer{buf: make([]byte, size), size: size}
}

// append 追加字节，必要时丢弃最旧数据。摊还 O(n)，无逐块分配。
func (r *ringBuffer) append(p []byte) {
	if len(p) == 0 {
		return
	}
	// 超过整个容量时，只保留尾部 size 字节。
	if len(p) >= r.size {
		copy(r.buf, p[len(p)-r.size:])
		r.start = 0
		r.count = r.size
		return
	}

	w := (r.start + r.count) % r.size
	n := copy(r.buf[w:], p)
	if n < len(p) {
		copy(r.buf, p[n:])
	}

	r.count += len(p)
	if r.count > r.size {
		over := r.count - r.size
		r.start = (r.start + over) % r.size
		r.count = r.size
	}
}

// snapshot 返回当前缓冲内容的拷贝（按时间顺序）。
func (r *ringBuffer) snapshot() []byte {
	out := make([]byte, r.count)
	if r.count == 0 {
		return out
	}
	if r.start+r.count <= r.size {
		copy(out, r.buf[r.start:r.start+r.count])
	} else {
		n := copy(out, r.buf[r.start:])
		copy(out[n:], r.buf[:r.count-n])
	}
	return out
}
