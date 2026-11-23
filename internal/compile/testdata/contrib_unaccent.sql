-- field: allDocuments
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $8), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'notes', __t_1."notes") AS ndata, row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."documents" AS __t_1
  WHERE lower("ext".unaccent('"ext"."unaccent"'::regdictionary, __t_1."notes")) = lower("ext".unaccent('"ext"."unaccent"'::regdictionary, $1::text)) AND "ext".unaccent('"ext"."unaccent"'::regdictionary, __t_1."notes") ILIKE "ext".unaccent('"ext"."unaccent"'::regdictionary, $2::text) AND "ext".unaccent('"ext"."unaccent"'::regdictionary, __t_1."notes") ILIKE "ext".unaccent('"ext"."unaccent"'::regdictionary, $3::text) AND "ext".unaccent('"ext"."unaccent"'::regdictionary, __t_1."notes") ILIKE "ext".unaccent('"ext"."unaccent"'::regdictionary, $4::text) AND ((__t_1."notes") OPERATOR("ext".%) $5::text AND "ext".unaccent('"ext"."unaccent"'::regdictionary, __t_1."notes") ILIKE "ext".unaccent('"ext"."unaccent"'::regdictionary, $6::text) OR __t_1."title" LIKE $7)
  ORDER BY __t_1."id" ASC
  LIMIT 101
) AS __s_2
GROUP BY ()
-- args: ["Crème Brûlée","%100\\%\\_%","caf_ %","Élan%","naive","%naïve%","x%",100]

