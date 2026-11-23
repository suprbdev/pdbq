-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."slug" ASC, __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE ((__t_1."slug" OPERATOR("ext".>) (SELECT __a."slug" FROM "public"."documents" AS __a WHERE __a."id" = $1) OR (__t_1."slug" IS NULL AND (SELECT __a."slug" FROM "public"."documents" AS __a WHERE __a."id" = $1) IS NOT NULL)) OR (coalesce(__t_1."slug" OPERATOR("ext".=) (SELECT __a."slug" FROM "public"."documents" AS __a WHERE __a."id" = $1), __t_1."slug" IS NULL AND (SELECT __a."slug" FROM "public"."documents" AS __a WHERE __a."id" = $1) IS NULL) AND __t_1."id" > $2))
  ORDER BY __t_1."slug" ASC, __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["3","3",3,2]

-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."path" DESC NULLS LAST, __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE ((__t_1."path" OPERATOR("ext".<) (SELECT __a."path" FROM "public"."documents" AS __a WHERE __a."id" = $1) OR (__t_1."path" IS NULL AND (SELECT __a."path" FROM "public"."documents" AS __a WHERE __a."id" = $1) IS NOT NULL)) OR (coalesce(__t_1."path" OPERATOR("ext".=) (SELECT __a."path" FROM "public"."documents" AS __a WHERE __a."id" = $1), __t_1."path" IS NULL AND (SELECT __a."path" FROM "public"."documents" AS __a WHERE __a."id" = $1) IS NULL) AND __t_1."id" > $2))
  ORDER BY __t_1."path" DESC NULLS LAST, __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["3","3",3,2]

-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id") AS ndata, row_number() OVER (ORDER BY __t_1."attrs" ASC, __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE ((__t_1."attrs" OPERATOR("ext".#>#) (SELECT __a."attrs" FROM "public"."documents" AS __a WHERE __a."id" = $1) OR (__t_1."attrs" IS NULL AND (SELECT __a."attrs" FROM "public"."documents" AS __a WHERE __a."id" = $1) IS NOT NULL)) OR (coalesce(__t_1."attrs" OPERATOR("ext".=) (SELECT __a."attrs" FROM "public"."documents" AS __a WHERE __a."id" = $1), __t_1."attrs" IS NULL AND (SELECT __a."attrs" FROM "public"."documents" AS __a WHERE __a."id" = $1) IS NULL) AND __t_1."id" > $2))
  ORDER BY __t_1."attrs" ASC, __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["3","3",3,2]

