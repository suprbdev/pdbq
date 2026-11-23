-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $8), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'pathNlevel', "ext".nlevel(__t_1."path"), 'pathParent', (CASE WHEN "ext".nlevel(__t_1."path") > 0 THEN "ext".subpath(__t_1."path", 0, "ext".nlevel(__t_1."path") - 1) END)::text, 'pathsLca', ("ext".lca(array_remove(__t_1."paths", NULL)))::text, 'tagIdsIcount', "ext".icount(__t_1."tag_ids"), 'color', ((__t_1."attrs") OPERATOR("ext".->) $1::text)::text, 'size', ((__t_1."attrs") OPERATOR("ext".->) $2::text)::text, 'pathLca', ("ext".lca(__t_1."path", $3::"ext".ltree))::text, 'rest', (CASE WHEN $4::int4 < "ext".nlevel(__t_1."path") AND -$4::int4 <= "ext".nlevel(__t_1."path") THEN "ext".subpath(__t_1."path", $4::int4, COALESCE($5::int4, 65535)) END)::text, 'last', (CASE WHEN $6::int4 < "ext".nlevel(__t_1."path") AND -$6::int4 <= "ext".nlevel(__t_1."path") THEN "ext".subpath(__t_1."path", $6::int4, COALESCE($7::int4, 65535)) END)::text) AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT 3
) AS __s_2
GROUP BY ()
-- args: ["color","size","a.b.x",1,null,-1,1,2]

-- field: documentById
SELECT json_build_object('pathNlevel', "ext".nlevel(__t_1."path"), 'attrsValue', ((__t_1."attrs") OPERATOR("ext".->) $1::text)::text) AS data
FROM "public"."documents" AS __t_1
WHERE __t_1."id" = $2
LIMIT 1
-- args: ["color",1]

