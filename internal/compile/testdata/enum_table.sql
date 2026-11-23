-- field: allEvents
SELECT json_build_object('nodes', coalesce(json_agg(__s_2.ndata ORDER BY __s_2.__rn ASC) FILTER (WHERE __s_2.__rn <= $4), '[]'::json)) AS data
FROM (
  SELECT json_build_object('id', __t_1."id", 'name', __t_1."name", 'typeCode', CASE __t_1."type_code" WHEN 'conference' THEN 'CONFERENCE' WHEN 'hackathon' THEN 'HACKATHON' WHEN 'meetup' THEN 'MEETUP' END) AS ndata, row_number() OVER (ORDER BY __t_1."type_code" ASC, __t_1."id" ASC) AS __rn
  FROM "public"."events" AS __t_1
  WHERE __t_1."type_code" = $1 AND __t_1."type_code" = ANY($2)
  ORDER BY __t_1."type_code" ASC, __t_1."id" ASC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["meetup",["conference","hackathon"],101,100]

