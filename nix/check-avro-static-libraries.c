#include <avro.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define CHECK(call) do { if ((call) != 0) { fprintf(stderr, "%s: %s\n", #call, avro_strerror()); return 1; } } while (0)
#define REQUIRE(condition) do { if (!(condition)) { fprintf(stderr, "failed: %s\n", #condition); return 1; } } while (0)

int main(void) {
    const char json[] = "{\"type\":\"record\",\"name\":\"fixture\",\"fields\":["
        "{\"name\":\"id\",\"type\":\"long\",\"field-id\":7},"
        "{\"name\":\"value\",\"type\":\"string\"}]}";
    avro_schema_t schema;
    CHECK(avro_schema_from_json_literal(json, &schema));
    REQUIRE(avro_schema_record_field_id(schema, 0) == 7);
    avro_schema_t timestamp, decimal;
    CHECK(avro_schema_from_json_literal("{\"type\":\"long\",\"logicalType\":\"timestamp-micros\",\"adjust-to-utc\":true}", &timestamp));
    REQUIRE(strcmp(avro_schema_logical_type(timestamp), "timestamp-micros") == 0);
    REQUIRE(avro_schema_adjust_to_utc(timestamp));
    CHECK(avro_schema_from_json_literal("{\"type\":\"fixed\",\"name\":\"amount\",\"size\":8,\"logicalType\":\"decimal\",\"precision\":18,\"scale\":2}", &decimal));
    REQUIRE(avro_schema_precision(decimal) == 18 && avro_schema_scale(decimal) == 2);
    avro_schema_decref(timestamp);
    avro_schema_decref(decimal);
    avro_value_iface_t *iface = avro_generic_class_from_schema(schema);
    REQUIRE(iface != NULL);
    const char *codecs[] = {"null", "deflate", "snappy", "lzma"};
    for (size_t c = 0; c < sizeof(codecs) / sizeof(codecs[0]); c++) {
        char path[64];
        REQUIRE(snprintf(path, sizeof(path), "fixture-%s.avro", codecs[c]) > 0);
        avro_value_t value, field;
        CHECK(avro_generic_value_new(iface, &value));
        avro_file_writer_t writer;
        CHECK(avro_file_writer_create_with_codec(path, schema, &writer, codecs[c], 512));
        for (int64_t i = 1; i <= 24; i++) {
            CHECK(avro_value_get_by_name(&value, "id", &field, NULL));
            CHECK(avro_value_set_long(&field, i));
            CHECK(avro_value_get_by_name(&value, "value", &field, NULL));
            CHECK(avro_value_set_string(&field, "static codec roundtrip"));
            CHECK(avro_file_writer_append_value(writer, &value));
        }
        CHECK(avro_file_writer_close(writer));
        avro_file_reader_t reader;
        CHECK(avro_file_reader(path, &reader));
        for (int64_t i = 1; i <= 24; i++) {
            int64_t id;
            const char *text;
            size_t length;
            CHECK(avro_file_reader_read_value(reader, &value));
            CHECK(avro_value_get_by_name(&value, "id", &field, NULL));
            CHECK(avro_value_get_long(&field, &id));
            REQUIRE(id == i);
            CHECK(avro_value_get_by_name(&value, "value", &field, NULL));
            CHECK(avro_value_get_string(&field, &text, &length));
            REQUIRE(strcmp(text, "static codec roundtrip") == 0 && length == strlen(text) + 1);
        }
        REQUIRE(avro_file_reader_read_value(reader, &value) != 0);
        CHECK(avro_file_reader_close(reader));
        avro_value_decref(&value);
        REQUIRE(remove(path) == 0);
    }
    avro_value_iface_decref(iface);
    avro_schema_decref(schema);
    puts("selected static Avro fork null/deflate/snappy/lzma codec roundtrips and logical-schema checks passed");
    return 0;
}
