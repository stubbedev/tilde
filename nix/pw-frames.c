#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <pipewire/pipewire.h>
#include <spa/param/video/format-utils.h>
#include <spa/param/buffers.h>
#include <spa/pod/builder.h>

struct capture {
	struct pw_main_loop *loop;
	struct pw_stream *stream;
	struct spa_video_info_raw format;
	FILE *out;
	int wanted;
	int written;
	int status;
};

static void fail(struct capture *c, const char *message) {
	fprintf(stderr, "pw-frames: %s\n", message);
	c->status = 1;
	pw_main_loop_quit(c->loop);
}

static void on_param_changed(void *data, uint32_t id, const struct spa_pod *param) {
	struct capture *c = data;
	uint32_t media_type, media_subtype;
	if (param == NULL || id != SPA_PARAM_Format) {
		return;
	}
	if (spa_format_parse(param, &media_type, &media_subtype) < 0 || media_type != SPA_MEDIA_TYPE_video || media_subtype != SPA_MEDIA_SUBTYPE_raw) {
		fail(c, "the stream offers no raw video");
		return;
	}
	if (spa_format_video_raw_parse(param, &c->format) < 0 || c->format.size.width == 0 || c->format.size.height == 0) {
		fail(c, "the stream has no usable raw video format");
		return;
	}
	uint8_t buffer[1024];
	struct spa_pod_builder b = SPA_POD_BUILDER_INIT(buffer, sizeof(buffer));
	const struct spa_pod *params[1];
	params[0] = spa_pod_builder_add_object(&b,
		SPA_TYPE_OBJECT_ParamBuffers, SPA_PARAM_Buffers,
		SPA_PARAM_BUFFERS_buffers, SPA_POD_CHOICE_RANGE_Int(4, 2, 16),
		SPA_PARAM_BUFFERS_dataType, SPA_POD_CHOICE_FLAGS_Int((1 << SPA_DATA_MemPtr) | (1 << SPA_DATA_MemFd)));
	pw_stream_update_params(c->stream, params, 1);
	printf("%u %u\n", c->format.size.width, c->format.size.height);
	fflush(stdout);
}

static void on_process(void *data) {
	struct capture *c = data;
	struct pw_buffer *b = pw_stream_dequeue_buffer(c->stream);
	if (b == NULL) {
		return;
	}
	struct spa_buffer *buf = b->buffer;
	struct spa_meta_header *h = spa_buffer_find_meta_data(buf, SPA_META_Header, sizeof(*h));
	struct spa_data *d = &buf->datas[0];
	uint32_t width = c->format.size.width, height = c->format.size.height;
	int corrupted = h != NULL && (h->flags & SPA_META_HEADER_FLAG_CORRUPTED);
	if (!corrupted && d->data != NULL && d->chunk->size > 0) {
		int32_t stride = d->chunk->stride > 0 ? d->chunk->stride : (int32_t)width * 4;
		if ((uint64_t)stride * (height - 1) + width * 4 > d->maxsize) {
			pw_stream_queue_buffer(c->stream, b);
			fail(c, "a buffer is smaller than its frame");
			return;
		}
		const uint8_t *pixels = (const uint8_t *)d->data + d->chunk->offset;
		for (uint32_t row = 0; row < height; row++) {
			if (fwrite(pixels + (size_t)row * stride, 4, width, c->out) != width) {
				pw_stream_queue_buffer(c->stream, b);
				fail(c, strerror(errno));
				return;
			}
		}
		if (++c->written == c->wanted) {
			pw_main_loop_quit(c->loop);
		}
	}
	pw_stream_queue_buffer(c->stream, b);
}

static void on_state_changed(void *data, enum pw_stream_state old, enum pw_stream_state state, const char *error) {
	struct capture *c = data;
	if (state == PW_STREAM_STATE_ERROR || (state == PW_STREAM_STATE_UNCONNECTED && old != PW_STREAM_STATE_UNCONNECTED)) {
		fail(c, error != NULL ? error : "the stream disconnected");
	}
}

static const struct pw_stream_events events = {
	PW_VERSION_STREAM_EVENTS,
	.state_changed = on_state_changed,
	.param_changed = on_param_changed,
	.process = on_process,
};

int main(int argc, char *argv[]) {
	if (argc != 5) {
		fprintf(stderr, "usage: pw-frames FD NODE FRAMES OUT\n");
		return 2;
	}
	pw_init(&argc, &argv);
	struct capture c = {.wanted = atoi(argv[3])};
	if ((c.out = fopen(argv[4], "wb")) == NULL) {
		perror("pw-frames: open the output");
		return 1;
	}
	c.loop = pw_main_loop_new(NULL);
	struct pw_context *context = pw_context_new(pw_main_loop_get_loop(c.loop), NULL, 0);
	struct pw_core *core = pw_context_connect_fd(context, atoi(argv[1]), NULL, 0);
	if (core == NULL) {
		perror("pw-frames: connect to the portal's PipeWire remote");
		return 1;
	}
	c.stream = pw_stream_new(core, "lys-wm-screencast-check",
		pw_properties_new(PW_KEY_MEDIA_TYPE, "Video", PW_KEY_MEDIA_CATEGORY, "Capture", PW_KEY_MEDIA_ROLE, "Screen", NULL));
	struct spa_hook listener;
	pw_stream_add_listener(c.stream, &listener, &events, &c);
	uint8_t buffer[1024];
	struct spa_pod_builder b = SPA_POD_BUILDER_INIT(buffer, sizeof(buffer));
	const struct spa_pod *params[1];
	params[0] = spa_pod_builder_add_object(&b,
		SPA_TYPE_OBJECT_Format, SPA_PARAM_EnumFormat,
		SPA_FORMAT_mediaType, SPA_POD_Id(SPA_MEDIA_TYPE_video),
		SPA_FORMAT_mediaSubtype, SPA_POD_Id(SPA_MEDIA_SUBTYPE_raw),
		SPA_FORMAT_VIDEO_format, SPA_POD_CHOICE_ENUM_Id(3, SPA_VIDEO_FORMAT_BGRx, SPA_VIDEO_FORMAT_BGRx, SPA_VIDEO_FORMAT_BGRA),
		SPA_FORMAT_VIDEO_size, SPA_POD_CHOICE_RANGE_Rectangle(&SPA_RECTANGLE(1280, 800), &SPA_RECTANGLE(1, 1), &SPA_RECTANGLE(8192, 8192)),
		SPA_FORMAT_VIDEO_framerate, SPA_POD_CHOICE_RANGE_Fraction(&SPA_FRACTION(30, 1), &SPA_FRACTION(0, 1), &SPA_FRACTION(1000, 1)));
	if (pw_stream_connect(c.stream, PW_DIRECTION_INPUT, (uint32_t)atoi(argv[2]), PW_STREAM_FLAG_AUTOCONNECT | PW_STREAM_FLAG_MAP_BUFFERS, params, 1) < 0) {
		fprintf(stderr, "pw-frames: connect the stream\n");
		return 1;
	}
	pw_main_loop_run(c.loop);
	spa_hook_remove(&listener);
	pw_stream_destroy(c.stream);
	pw_core_disconnect(core);
	pw_context_destroy(context);
	pw_main_loop_destroy(c.loop);
	pw_deinit();
	if (fclose(c.out) != 0) {
		perror("pw-frames: close the output");
		return 1;
	}
	return c.status;
}
