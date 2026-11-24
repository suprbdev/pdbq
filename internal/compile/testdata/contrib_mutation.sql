-- field: createDocument
WITH __mut AS (
  INSERT INTO "public"."documents" ("attrs", "avail", "during", "ip", "net", "path", "paths", "price", "search", "slug", "span", "title")
  VALUES ("ext".hstore($1::text[]), int4multirange(VARIADIC ARRAY(SELECT int4range(__l, __u, __b) FROM unnest($2::int4[], $3::int4[], $4::text[]) AS __r(__l, __u, __b))), tstzrange($5::timestamptz, $6::timestamptz, $7::text), $8::inet, $9::cidr, $10::"ext".ltree, $11::"ext".ltree[], numrange($12::numeric, $13::numeric, $14::text), $15::tsvector, $16::"ext".citext, int4range($17::int4, $18::int4, $19::text), $20)
  RETURNING "id", "attrs", "span"
)
SELECT json_build_object('document', json_build_object('id', __t_1."id", 'attrs', "ext".hstore_to_json(__t_1."attrs"), 'span', (SELECT json_build_object('start', CASE WHEN lower(__rg_2.r) IS NULL THEN NULL ELSE json_build_object('value', lower(__rg_2.r), 'inclusive', lower_inc(__rg_2.r)) END, 'end', CASE WHEN upper(__rg_2.r) IS NULL THEN NULL ELSE json_build_object('value', upper(__rg_2.r), 'inclusive', upper_inc(__rg_2.r)) END) FROM (SELECT __t_1."span" AS r) AS __rg_2 WHERE __rg_2.r IS NOT NULL))) AS data
FROM __mut AS __t_1
-- args: [["color","red","size",null],["1","7"],["3","9"],["[)","[]"],"2024-01-01T00:00:00Z",null,"[)","192.168.0.1","10.0.0.0/8","a.b.c",["a.b","x"],"9.99","19.99","[]","'cat':1 'dog':2","Hello","1","10","[)","t"]

-- field: updateDocumentBySlug
WITH __mut AS (
  UPDATE "public"."documents"
  SET "attrs" = "ext".hstore($1::text[]), "avail" = int4multirange(VARIADIC ARRAY(SELECT int4range(__l, __u, __b) FROM unnest($2::int4[], $3::int4[], $4::text[]) AS __r(__l, __u, __b))), "span" = int4range($5::int4, $6::int4, $7::text)
  WHERE "public"."documents"."slug" OPERATOR("ext".=) $8::"ext".citext
  RETURNING "id"
)
SELECT json_build_object('document', json_build_object('id', __t_1."id")) AS data
FROM __mut AS __t_1
-- args: [[],[],[],[],null,null,"()","Hello"]

-- field: updateManyDocuments
WITH __mut AS (
  UPDATE "public"."documents"
  SET "path" = $1::"ext".ltree
  WHERE ("public"."documents"."attrs") OPERATOR("ext".?) $2::text
  RETURNING 1
)
SELECT json_build_object('affectedCount', (SELECT count(*) FROM __mut)) AS data
-- args: ["archive","stale"]

-- field: createDocument
WITH __mut AS (
  INSERT INTO "public"."documents" ("span", "stay", "title")
  VALUES (int4range($1::int4, $2::int4, $3::text), jsonb_populate_record(NULL::"public"."stay", $4::jsonb), $5)
  RETURNING "span", "stay"
)
SELECT json_build_object('document', json_build_object('span', (SELECT json_build_object('isEmpty', isempty(__rg_2.r)) FROM (SELECT __t_1."span" AS r) AS __rg_2 WHERE __rg_2.r IS NOT NULL), 'stay', CASE WHEN (__t_1."stay")::text IS NULL THEN NULL ELSE json_build_object('nights', (SELECT json_build_object('start', CASE WHEN lower(__rg_3.r) IS NULL THEN NULL ELSE json_build_object('value', lower(__rg_3.r)) END) FROM (SELECT (__t_1."stay")."nights" AS r) AS __rg_3 WHERE __rg_3.r IS NOT NULL), 'extras', "ext".hstore_to_json((__t_1."stay")."extras")) END)) AS data
FROM __mut AS __t_1
-- args: ["0","0","[)","{\"extras\":\"\\\"a\\\"=\\u003e\\\"x \\\\\\\"y\\\\\\\"\\\", \\\"b\\\"=\\u003eNULL\",\"nights\":\"[\\\"1\\\",\\\"3\\\")\"}","e"]

