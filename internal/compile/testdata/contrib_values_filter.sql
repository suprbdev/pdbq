-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $3), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE "ext".nlevel(__t_1."path") >= $1 AND "ext".nlevel(__t_1."path") < $2
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: [2,5,100]

-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE ((__t_1."attrs") OPERATOR("ext".->) $1::text) = ANY($2) AND ((__t_1."attrs") OPERATOR("ext".->) $1::text) ILIKE $3
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: ["color",["red","blue"],"r%",100]

-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: [100]

-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE (((__t_1."attrs") OPERATOR("ext".->) $1::text) = $2 AND NOT (((__t_1."attrs") OPERATOR("ext".->) $3::text) IS NULL))
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: ["color","red","size",100]

-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE __t_1."tag_ids" @> $1 AND (__t_1."tag_ids") OPERATOR("ext".@@) $2::"ext".query_int AND "ext".icount(__t_1."tag_ids") > $3
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: [[1],"1\u0026(2|!3)",1,100]

-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $6), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE "ext".dmetaphone(__t_1."notes") = "ext".dmetaphone($1::text) AND CASE WHEN char_length((__t_1."notes")::text) <= 255 THEN "ext".levenshtein_less_equal((__t_1."notes")::text, $2::text, $3::int4) END < $3::int4 AND "ext".metaphone(CASE WHEN octet_length((__t_1."notes")::text) <= 255 THEN (__t_1."notes")::text END, 255) = "ext".metaphone($4::text, 255) AND "ext".soundex(__t_1."notes") = "ext".soundex($5::text)
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: ["Smith","kitten",3,"Knight","Robert",100]

