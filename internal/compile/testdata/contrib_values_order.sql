-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'pathNlevel', "ext".nlevel(__t_1."path")) AS ndata, row_number() OVER (ORDER BY "ext".nlevel(__t_1."path") DESC NULLS LAST, __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  ORDER BY "ext".nlevel(__t_1."path") DESC NULLS LAST, __t_1."id" ASC
  LIMIT 4
) AS __s_2
GROUP BY ()
-- args: [3]

-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'attrsValue', ((__t_1."attrs") OPERATOR("ext".->) $1::text)::text) AS ndata, row_number() OVER (ORDER BY (__t_1."attrs") OPERATOR("ext".->) $2::text ASC, __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE (((__t_1."attrs") OPERATOR("ext".->) $2::text > (SELECT (__a."attrs") OPERATOR("ext".->) $2::text FROM "public"."documents" AS __a WHERE __a."id" = $3) OR ((__t_1."attrs") OPERATOR("ext".->) $2::text IS NULL AND (SELECT (__a."attrs") OPERATOR("ext".->) $2::text FROM "public"."documents" AS __a WHERE __a."id" = $3) IS NOT NULL)) OR ((__t_1."attrs") OPERATOR("ext".->) $2::text IS NOT DISTINCT FROM (SELECT (__a."attrs") OPERATOR("ext".->) $2::text FROM "public"."documents" AS __a WHERE __a."id" = $3) AND __t_1."id" > $4))
  ORDER BY (__t_1."attrs") OPERATOR("ext".->) $2::text ASC, __t_1."id" ASC
  LIMIT 3
) AS __s_2
GROUP BY ()
-- args: ["color","color","3","3",2]

-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $5), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'notesLevenshteinDistance', CASE WHEN char_length((__t_1."notes")::text) <= 255 THEN "ext".levenshtein((__t_1."notes")::text, $1::text) END) AS ndata, row_number() OVER (ORDER BY CASE WHEN char_length((__t_1."notes")::text) <= 255 THEN "ext".levenshtein((__t_1."notes")::text, $2::text) END ASC, __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE ((CASE WHEN char_length((__t_1."notes")::text) <= 255 THEN "ext".levenshtein((__t_1."notes")::text, $2::text) END > (SELECT CASE WHEN char_length((__a."notes")::text) <= 255 THEN "ext".levenshtein((__a."notes")::text, $2::text) END FROM "public"."documents" AS __a WHERE __a."id" = $3) OR (CASE WHEN char_length((__t_1."notes")::text) <= 255 THEN "ext".levenshtein((__t_1."notes")::text, $2::text) END IS NULL AND (SELECT CASE WHEN char_length((__a."notes")::text) <= 255 THEN "ext".levenshtein((__a."notes")::text, $2::text) END FROM "public"."documents" AS __a WHERE __a."id" = $3) IS NOT NULL)) OR (CASE WHEN char_length((__t_1."notes")::text) <= 255 THEN "ext".levenshtein((__t_1."notes")::text, $2::text) END IS NOT DISTINCT FROM (SELECT CASE WHEN char_length((__a."notes")::text) <= 255 THEN "ext".levenshtein((__a."notes")::text, $2::text) END FROM "public"."documents" AS __a WHERE __a."id" = $3) AND __t_1."id" > $4))
  ORDER BY CASE WHEN char_length((__t_1."notes")::text) <= 255 THEN "ext".levenshtein((__t_1."notes")::text, $2::text) END ASC, __t_1."id" ASC
  LIMIT 3
) AS __s_2
GROUP BY ()
-- args: ["kitten","kitten","3","3",2]

-- field: allDocuments
-- error: compile: orderBy ATTRS_VALUE_DESC requires a hstoreKey: value for the same column

-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $1), '[]'::json)) AS data
FROM (
  SELECT json_build_object('notesLevenshteinDistance', NULL::float8) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT 2
) AS __s_2
GROUP BY ()
-- args: [1]

