#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <expat.h>
#include <minizip-ng/mz.h>
#include <minizip-ng/mz_strm.h>
#include <minizip-ng/mz_zip.h>
#include <minizip-ng/mz_zip_rw.h>

static void XMLCALL element(void *state, const XML_Char *name, const XML_Char **attributes) {
    (void)attributes;
    if (strcmp(name, "worksheet") == 0) (*(int *)state)++;
}

int main(void) {
    const char xml[] = "<worksheet><sheetData><row><c><v>42</v></c></row></sheetData></worksheet>";
    void *writer = mz_zip_writer_create();
    if (!writer) return 1;
    mz_zip_writer_set_compress_method(writer, MZ_COMPRESS_METHOD_DEFLATE);
    mz_zip_file info = {0};
    info.filename = "xl/worksheets/sheet1.xml";
    info.compression_method = MZ_COMPRESS_METHOD_DEFLATE;
    if (mz_zip_writer_open_file(writer, "selected-libraries.xlsx", 0, 0) != MZ_OK ||
        mz_zip_writer_entry_open(writer, &info) != MZ_OK ||
        mz_zip_writer_entry_write(writer, xml, sizeof(xml) - 1) != sizeof(xml) - 1 ||
        mz_zip_writer_entry_close(writer) != MZ_OK || mz_zip_writer_close(writer) != MZ_OK) return 2;
    mz_zip_writer_delete(&writer);
    void *reader = mz_zip_reader_create();
    if (!reader || mz_zip_reader_open_file(reader, "selected-libraries.xlsx") != MZ_OK ||
        mz_zip_reader_goto_first_entry(reader) != MZ_OK || mz_zip_reader_entry_open(reader) != MZ_OK) return 3;
    char restored[sizeof(xml)] = {0};
    if (mz_zip_reader_entry_read(reader, restored, sizeof(restored)) != sizeof(xml) - 1 ||
        strcmp(restored, xml) != 0 || mz_zip_reader_entry_close(reader) != MZ_OK ||
        mz_zip_reader_close(reader) != MZ_OK) return 4;
    mz_zip_reader_delete(&reader);
    XML_Parser parser = XML_ParserCreate(NULL);
    if (!parser) return 5;
    int worksheets = 0;
    XML_SetUserData(parser, &worksheets);
    XML_SetStartElementHandler(parser, element);
    if (XML_Parse(parser, restored, sizeof(xml) - 1, XML_TRUE) != XML_STATUS_OK || worksheets != 1) return 6;
    XML_ParserFree(parser);
    puts("selected static Expat/minizip/zlib deflated worksheet roundtrip passed");
    return 0;
}
