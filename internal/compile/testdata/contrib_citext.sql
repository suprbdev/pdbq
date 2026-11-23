-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $10), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'slug', __t_1."slug") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE __t_1."slug" OPERATOR("ext".~~) $1::"ext".citext AND __t_1."slug" OPERATOR("ext".=) $2::"ext".citext AND __t_1."slug" OPERATOR("ext".>) $3::"ext".citext AND __t_1."slug" OPERATOR("ext".=) ANY($4::text[]::"ext".citext[]) AND __t_1."slug" OPERATOR("ext".~~) $5::"ext".citext AND __t_1."slug" OPERATOR("ext".~~*) $6::"ext".citext AND __t_1."slug" OPERATOR("ext".<>) ALL($7::text[]::"ext".citext[]) AND __t_1."slug" OPERATOR("ext".~~) $8::"ext".citext
  ORDER BY __t_1."id" ASC
  LIMIT $9
) AS __s_2
GROUP BY ()
-- args: ["%\\%o","Hello","a",["A","b"],"he%","%LO",["c"],"he\\_%",101,100]

-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $7), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE __t_1."attrs" OPERATOR("ext".=) "ext".hstore($1::text[]) AND __t_1."slug" OPERATOR("ext".=) $2::"ext".citext AND __t_1."span" = int4range($3::int4, $4::int4, $5::text)
  ORDER BY __t_1."id" ASC
  LIMIT $6
) AS __s_2
GROUP BY ()
-- args: [["a","1"],"HELLO","1",null,"[)",101,100]

-- field: documentBySlug
SELECT json_build_object('id', __t_1."id") AS data
FROM "public"."documents" AS __t_1
WHERE __t_1."slug" OPERATOR("ext".=) $1::"ext".citext
LIMIT 1
-- args: ["Hello"]

